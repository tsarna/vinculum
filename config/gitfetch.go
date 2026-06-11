package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
)

// cloneAndFetch clones the repository described by def into a process-private
// working directory at the selected revision, then materializes each fetch's
// subtree into its destination. The working clone is always removed when the
// function returns; it is never exposed as a fetch destination.
func cloneAndFetch(def *GitDefinition, logger *zap.Logger) hcl.Diagnostics {
	workdir, err := os.MkdirTemp("", "vinculum-git-*")
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git clone failed",
			Detail:   fmt.Sprintf("Git block %q: creating work directory: %s", def.Label, err),
			Subject:  &def.DefRange,
		}}
	}
	defer os.RemoveAll(workdir)

	if diags := cloneRepo(def, workdir, logger); diags.HasErrors() {
		return diags
	}

	return materializeFetches(def, workdir, logger)
}

// cloneRepo clones def's repository into workdir at the selected revision and
// depth, recursing submodules when requested. When a commit is pinned it is
// checked out after the clone. All failures are fatal diagnostics.
func cloneRepo(def *GitDefinition, workdir string, logger *zap.Logger) hcl.Diagnostics {
	auth, diags := buildAuthMethod(def, logger)
	if diags.HasErrors() {
		return diags
	}

	opts := &git.CloneOptions{
		URL:  def.Repo,
		Auth: auth,
	}

	// Depth: default shallow (1); explicit 0 means full history.
	depth := 1
	if def.Depth != nil {
		depth = *def.Depth
	}
	opts.Depth = depth

	// Revision selection. branch/tag/commit are mutually exclusive (validated
	// earlier). A pinned commit clones the default ref then checks out the SHA.
	switch {
	case def.Branch != "":
		opts.ReferenceName = plumbing.NewBranchReferenceName(def.Branch)
		opts.SingleBranch = true
	case def.Tag != "":
		opts.ReferenceName = plumbing.NewTagReferenceName(def.Tag)
		opts.SingleBranch = true
	}

	if def.Submodules {
		opts.RecurseSubmodules = git.DefaultSubmoduleRecursionDepth
	} else {
		opts.RecurseSubmodules = git.NoRecurseSubmodules
	}

	if logger != nil {
		logger.Info("git clone starting",
			zap.String("block", def.Label),
			zap.String("repo", def.Repo),
			zap.String("revision", revisionDesc(def)),
			zap.Int("depth", depth))
	}

	repo, err := git.PlainCloneContext(context.Background(), workdir, false, opts)
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git clone failed",
			Detail:   fmt.Sprintf("Git block %q: cloning %s: %s", def.Label, def.Repo, err),
			Subject:  &def.DefRange,
		}}
	}

	if def.Commit != "" {
		if cdiags := checkoutCommit(def, repo); cdiags.HasErrors() {
			return cdiags
		}
	}

	if logger != nil {
		logger.Info("git clone complete", zap.String("block", def.Label), zap.String("repo", def.Repo))
	}

	return nil
}

// checkoutCommit checks out a pinned commit SHA in the freshly cloned repo. If
// the object is not present (typical for a shallow clone of an arbitrary
// historical commit), the error names the commit and suggests raising depth.
func checkoutCommit(def *GitDefinition, repo *git.Repository) hcl.Diagnostics {
	r := attrRangeForCommit(def)
	wt, err := repo.Worktree()
	if err != nil {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git checkout failed",
			Detail:   fmt.Sprintf("Git block %q: opening worktree: %s", def.Label, err),
			Subject:  &r,
		}}
	}

	err = wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(def.Commit)})
	if err != nil {
		detail := fmt.Sprintf("Git block %q: checking out commit %s: %s", def.Label, def.Commit, err)
		if isObjectNotFound(err) {
			detail += ".\nThe commit is not present in the clone; it may be unreachable " +
				"within the configured depth. Raise `depth` or set `depth = 0` for a full clone."
		}
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git checkout failed",
			Detail:   detail,
			Subject:  &r,
		}}
	}
	return nil
}

// isObjectNotFound reports whether err indicates the requested object is absent
// from the (possibly shallow) clone.
func isObjectNotFound(err error) bool {
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "object not found") ||
		strings.Contains(msg, "reference not found")
}

// buildAuthMethod constructs the go-git auth method appropriate for the repo's
// transport. Returns (nil, nil) for anonymous access. Credential values are
// never logged.
func buildAuthMethod(def *GitDefinition, logger *zap.Logger) (transport.AuthMethod, hcl.Diagnostics) {
	if def.Auth == nil {
		return nil, nil
	}
	a := def.Auth

	switch inferGitTransport(def.Repo) {
	case gitTransportHTTP:
		switch {
		case a.Token != "":
			// PAT shorthand: token as the password, a placeholder username,
			// which is what GitHub/GitLab/Gitea expect.
			return &githttp.BasicAuth{Username: "vinculum", Password: a.Token}, nil
		case a.Username != "" || a.Password != "":
			return &githttp.BasicAuth{Username: a.Username, Password: a.Password}, nil
		default:
			return nil, nil
		}
	case gitTransportSSH:
		return buildSSHAuth(def, logger)
	default:
		// local/unknown transport takes no credentials (validated earlier).
		return nil, nil
	}
}

// buildSSHAuth builds an SSH public-key auth method with host-key verification.
func buildSSHAuth(def *GitDefinition, logger *zap.Logger) (transport.AuthMethod, hcl.Diagnostics) {
	a := def.Auth
	user := sshUser(def.Repo)

	var (
		pk  *gitssh.PublicKeys
		err error
	)
	switch {
	case a.PrivateKey != "":
		pk, err = gitssh.NewPublicKeys(user, []byte(a.PrivateKey), a.Passphrase)
	case a.PrivateKeyFile != "":
		pk, err = gitssh.NewPublicKeysFromFile(user, a.PrivateKeyFile, a.Passphrase)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git ssh auth requires a key",
			Detail: fmt.Sprintf(
				"Git block %q uses an SSH repo but provides no private_key or private_key_file.",
				def.Label),
			Subject: &a.DefRange,
		}}
	}
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "git ssh key error",
			Detail:   fmt.Sprintf("Git block %q: loading SSH private key: %s", def.Label, err),
			Subject:  &a.DefRange,
		}}
	}

	cb, diags := sshHostKeyCallback(def, logger)
	if diags.HasErrors() {
		return nil, diags
	}
	pk.HostKeyCallback = cb
	return pk, nil
}

// sshHostKeyCallback resolves the host-key verification policy: an explicit
// known_hosts file, an explicit insecure override (logged), or the default
// user known_hosts file. If none is available, verification cannot be done and
// the fetch fails with guidance.
func sshHostKeyCallback(def *GitDefinition, logger *zap.Logger) (gossh.HostKeyCallback, hcl.Diagnostics) {
	a := def.Auth

	if a.InsecureIgnoreHostKey {
		if logger != nil {
			logger.Warn("git ssh host-key verification disabled (insecure_ignore_host_key = true)")
		}
		return gossh.InsecureIgnoreHostKey(), nil
	}

	if a.KnownHosts != "" {
		cb, err := gitssh.NewKnownHostsCallback(a.KnownHosts)
		if err != nil {
			r := authAttrRangeForKnownHosts(def)
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "git known_hosts error",
				Detail:   fmt.Sprintf("Git block %q: reading known_hosts %q: %s", def.Label, a.KnownHosts, err),
				Subject:  &r,
			}}
		}
		return cb, nil
	}

	// Fall back to the default user known_hosts file when it exists.
	if home, err := os.UserHomeDir(); err == nil {
		def := home + "/.ssh/known_hosts"
		if _, statErr := os.Stat(def); statErr == nil {
			cb, cbErr := gitssh.NewKnownHostsCallback(def)
			if cbErr == nil {
				return cb, nil
			}
		}
	}

	r := def.Auth.DefRange
	return nil, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "git ssh host-key verification unconfigured",
		Detail: fmt.Sprintf(
			"Git block %q uses SSH but no known_hosts is set and no default "+
				"$HOME/.ssh/known_hosts exists. Set `known_hosts`, or set "+
				"`insecure_ignore_host_key = true` to disable verification.",
			def.Label),
		Subject: &r,
	}}
}

// sshUser extracts the SSH login user from a repo URL, defaulting to "git".
func sshUser(repo string) string {
	rest := strings.TrimPrefix(repo, "ssh://")
	if at := strings.Index(rest, "@"); at > 0 {
		return rest[:at]
	}
	return "git"
}

// revisionDesc returns a short human description of the selected revision for
// logging (no credentials).
func revisionDesc(def *GitDefinition) string {
	switch {
	case def.Branch != "":
		return "branch " + def.Branch
	case def.Tag != "":
		return "tag " + def.Tag
	case def.Commit != "":
		return "commit " + def.Commit
	default:
		return "default branch"
	}
}

// attrRangeForCommit / authAttrRangeForKnownHosts give better Subjects without
// threading the *hcl.Block all the way down; they fall back to the auth/def
// range, which is acceptable for runtime (post-validation) errors.
func attrRangeForCommit(def *GitDefinition) hcl.Range {
	return def.DefRange
}

func authAttrRangeForKnownHosts(def *GitDefinition) hcl.Range {
	if def.Auth != nil {
		return def.Auth.DefRange
	}
	return def.DefRange
}
