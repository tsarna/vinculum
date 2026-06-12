package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"go.uber.org/zap"
)

// GitDefinition is the structural decode of a `git "<label>" { ... }` block in
// a .vinit file. It clones a remote repository during bootstrap pass 1 and
// materializes one or more subtrees onto the local filesystem before any .vcl
// is parsed.
type GitDefinition struct {
	Label      string     `hcl:",label"`
	Disabled   bool       `hcl:"disabled,optional"`
	Repo       string     `hcl:"repo"`
	Branch     string     `hcl:"branch,optional"`
	Tag        string     `hcl:"tag,optional"`
	Commit     string     `hcl:"commit,optional"`
	Depth      *int       `hcl:"depth,optional"`
	Submodules bool       `hcl:"submodules,optional"`
	Auth       *GitAuth   `hcl:"auth,block"`
	Fetches    []GitFetch `hcl:"fetch,block"`
	DefRange   hcl.Range  `hcl:",def_range"`
}

// GitAuth holds the optional credentials for a git block. Which attributes are
// valid depends on the transport inferred from the repo URL scheme (HTTP(S) vs
// SSH); see validateGitAuth.
type GitAuth struct {
	Token                 string    `hcl:"token,optional"`
	Username              string    `hcl:"username,optional"`
	Password              string    `hcl:"password,optional"`
	PrivateKey            string    `hcl:"private_key,optional"`
	PrivateKeyFile        string    `hcl:"private_key_file,optional"`
	Passphrase            string    `hcl:"passphrase,optional"`
	KnownHosts            string    `hcl:"known_hosts,optional"`
	InsecureIgnoreHostKey bool      `hcl:"insecure_ignore_host_key,optional"`
	DefRange              hcl.Range `hcl:",def_range"`
}

// GitFetch copies one subtree of the cloned repository to one local
// destination. A git block has one or more fetch sub-blocks sharing a single
// clone.
type GitFetch struct {
	Name      string    `hcl:",label"`
	From      string    `hcl:"from,optional"`
	Into      string    `hcl:"into"`
	Overwrite bool      `hcl:"overwrite,optional"`
	DefRange  hcl.Range `hcl:",def_range"`
}

// git transport classes inferred from the repo URL scheme.
const (
	gitTransportHTTP  = "http"
	gitTransportSSH   = "ssh"
	gitTransportLocal = "local"
)

// processGitBlock validates a single `git "<label>" { ... }` block, evaluates
// its `disabled` attribute, and (unless disabled) clones the repository and
// materializes its fetches. Mirrors processPluginBlock.
func processGitBlock(
	block *hcl.Block,
	evalCtx *hcl.EvalContext,
	logger *zap.Logger,
	seen map[string]hcl.Range,
) hcl.Diagnostics {
	var def GitDefinition
	diags := gohcl.DecodeBody(block.Body, evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}
	def.Label = block.Labels[0]

	labelRange := block.LabelRanges[0]

	// The git label has no filesystem meaning (unlike a plugin label), so it
	// carries no character restriction beyond HCL's own. Only uniqueness is
	// enforced.
	if prev, dup := seen[def.Label]; dup {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate git label",
			Detail: fmt.Sprintf(
				"Git block %q was already declared at %s.",
				def.Label, prev),
			Subject: &labelRange,
		}}
	}
	seen[def.Label] = labelRange

	if def.Disabled {
		return nil
	}

	if vdiags := validateGitBlock(block, &def); vdiags.HasErrors() {
		return vdiags
	}

	gitLogger := logger
	if gitLogger != nil {
		gitLogger = gitLogger.With(zap.String("git_block", def.Label))
	}

	return cloneAndFetch(&def, gitLogger)
}

// validateGitBlock performs all static (pre-clone) validation: revision
// mutual-exclusion, fetch presence and path safety, and auth consistency with
// the inferred transport. All failures are fatal with a Subject pointing at the
// offending attribute where possible.
func validateGitBlock(block *hcl.Block, def *GitDefinition) hcl.Diagnostics {
	var diags hcl.Diagnostics

	// repo is required by the schema, but a value that interpolates to empty
	// (e.g. an unset env var) slips past the decode.
	if strings.TrimSpace(def.Repo) == "" {
		r := attrRange(block, "repo")
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "git block requires repo",
			Detail:   fmt.Sprintf("Git block %q has an empty repo URL.", def.Label),
			Subject:  &r,
		})
		return diags
	}

	// Revision selection: branch / tag / commit are mutually exclusive.
	var revAttrs []string
	if def.Branch != "" {
		revAttrs = append(revAttrs, "branch")
	}
	if def.Tag != "" {
		revAttrs = append(revAttrs, "tag")
	}
	if def.Commit != "" {
		revAttrs = append(revAttrs, "commit")
	}
	if len(revAttrs) > 1 {
		// Point at the second-specified attribute as the conflicting one.
		r := attrRange(block, revAttrs[1])
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Conflicting git revision",
			Detail: fmt.Sprintf(
				"Git block %q sets more than one of branch/tag/commit (%s); they are mutually exclusive.",
				def.Label, strings.Join(revAttrs, ", ")),
			Subject: &r,
		})
	}

	// At least one fetch is required.
	if len(def.Fetches) == 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "git block requires a fetch",
			Detail: fmt.Sprintf(
				"Git block %q has no fetch sub-blocks; a clone with no destination does nothing.",
				def.Label),
			Subject: &def.DefRange,
		})
	}

	for i := range def.Fetches {
		f := &def.Fetches[i]
		if strings.TrimSpace(f.Into) == "" {
			r := fetchAttrRange(block, f.Name, "into")
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "git fetch requires into",
				Detail: fmt.Sprintf(
					"Fetch %q in git block %q has an empty into path.",
					f.Name, def.Label),
				Subject: &r,
			})
		}
		if err := validateFromPath(f.From); err != nil {
			r := fetchAttrRange(block, f.Name, "from")
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid git fetch from path",
				Detail: fmt.Sprintf(
					"Fetch %q in git block %q: %s",
					f.Name, def.Label, err),
				Subject: &r,
			})
		}
	}

	diags = append(diags, validateGitAuth(block, def)...)

	return diags
}

// validateFromPath checks that a fetch `from` is repo-relative and cannot
// escape the repository root: no absolute prefix and no ".." component. An
// empty from is valid and defaults to "." (the repo root) at fetch time.
func validateFromPath(from string) error {
	if from == "" {
		return nil
	}
	if filepath.IsAbs(from) || strings.HasPrefix(from, "/") {
		return fmt.Errorf("from %q must be repo-relative (no leading '/')", from)
	}
	clean := filepath.ToSlash(filepath.Clean(from))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("from %q escapes the repository root", from)
	}
	return nil
}

// inferGitTransport classifies a repo URL by transport. HTTP(S) and SSH (both
// ssh:// and scp-style git@host:path) determine which auth attributes are
// valid; anything else (file://, a bare path) is treated as a local clone that
// takes no credentials.
func inferGitTransport(repo string) string {
	switch {
	case strings.HasPrefix(repo, "https://"), strings.HasPrefix(repo, "http://"):
		return gitTransportHTTP
	case strings.HasPrefix(repo, "ssh://"):
		return gitTransportSSH
	case isSCPStyleURL(repo):
		return gitTransportSSH
	default:
		return gitTransportLocal
	}
}

// isSCPStyleURL reports whether repo is an scp-style SSH reference such as
// git@github.com:org/repo.git — a "user@host:path" form with no scheme and a
// colon before the first slash.
func isSCPStyleURL(repo string) bool {
	if strings.Contains(repo, "://") {
		return false
	}
	at := strings.Index(repo, "@")
	colon := strings.Index(repo, ":")
	slash := strings.Index(repo, "/")
	if at < 0 || colon < 0 {
		return false
	}
	// colon must come after the host and before any path slash.
	if slash >= 0 && colon > slash {
		return false
	}
	return colon > at
}

// validateGitAuth enforces that the supplied auth attributes are consistent
// with the inferred transport and not internally contradictory.
func validateGitAuth(block *hcl.Block, def *GitDefinition) hcl.Diagnostics {
	if def.Auth == nil {
		return nil
	}
	a := def.Auth
	transport := inferGitTransport(def.Repo)

	httpSet := a.Token != "" || a.Username != "" || a.Password != ""
	sshSet := a.PrivateKey != "" || a.PrivateKeyFile != "" || a.Passphrase != "" ||
		a.KnownHosts != "" || a.InsecureIgnoreHostKey

	var diags hcl.Diagnostics
	mismatch := func(attr, want string) {
		r := authAttrRange(block, attr)
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Auth attribute does not match transport",
			Detail: fmt.Sprintf(
				"Git block %q uses a %s repo, but %q is a %s auth attribute.",
				def.Label, transport, attr, want),
			Subject: &r,
		})
	}
	conflict := func(attr, detail string) {
		r := authAttrRange(block, attr)
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Conflicting git auth attributes",
			Detail:   detail,
			Subject:  &r,
		})
	}

	switch transport {
	case gitTransportHTTP:
		if a.PrivateKey != "" {
			mismatch("private_key", "SSH")
		}
		if a.PrivateKeyFile != "" {
			mismatch("private_key_file", "SSH")
		}
		if a.Passphrase != "" {
			mismatch("passphrase", "SSH")
		}
		if a.KnownHosts != "" {
			mismatch("known_hosts", "SSH")
		}
		if a.InsecureIgnoreHostKey {
			mismatch("insecure_ignore_host_key", "SSH")
		}
	case gitTransportSSH:
		if a.Token != "" {
			mismatch("token", "HTTP(S)")
		}
		if a.Username != "" {
			mismatch("username", "HTTP(S)")
		}
		if a.Password != "" {
			mismatch("password", "HTTP(S)")
		}
	default: // local: no credentials apply.
		if httpSet || sshSet {
			r := def.Auth.DefRange
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Auth attribute does not match transport",
				Detail: fmt.Sprintf(
					"Git block %q uses a local/unknown repo URL that takes no credentials.",
					def.Label),
				Subject: &r,
			})
		}
	}

	// Internal conflicts, independent of transport.
	if a.Token != "" && (a.Username != "" || a.Password != "") {
		conflict("token", fmt.Sprintf(
			"Git block %q sets token together with username/password; they are mutually exclusive.",
			def.Label))
	}
	if a.PrivateKey != "" && a.PrivateKeyFile != "" {
		conflict("private_key_file", fmt.Sprintf(
			"Git block %q sets both private_key and private_key_file; they are mutually exclusive.",
			def.Label))
	}
	if a.KnownHosts != "" && a.InsecureIgnoreHostKey {
		conflict("insecure_ignore_host_key", fmt.Sprintf(
			"Git block %q sets both known_hosts and insecure_ignore_host_key; they are mutually exclusive.",
			def.Label))
	}

	return diags
}

// attrRange returns the source range of a top-level attribute in the git block
// body, falling back to the block's definition range when unavailable.
func attrRange(block *hcl.Block, name string) hcl.Range {
	if body, ok := block.Body.(*hclsyntax.Body); ok {
		if attr, ok := body.Attributes[name]; ok {
			return attr.SrcRange
		}
	}
	return block.DefRange
}

// authAttrRange returns the source range of an attribute inside the git block's
// auth sub-block, falling back to the block's definition range.
func authAttrRange(block *hcl.Block, name string) hcl.Range {
	if body, ok := block.Body.(*hclsyntax.Body); ok {
		for _, b := range body.Blocks {
			if b.Type == "auth" {
				if attr, ok := b.Body.Attributes[name]; ok {
					return attr.SrcRange
				}
				return b.DefRange()
			}
		}
	}
	return block.DefRange
}

// fetchAttrRange returns the source range of an attribute inside the named
// fetch sub-block, falling back to the fetch block (or git block) range.
func fetchAttrRange(block *hcl.Block, fetchLabel, name string) hcl.Range {
	if body, ok := block.Body.(*hclsyntax.Body); ok {
		for _, b := range body.Blocks {
			if b.Type == "fetch" && len(b.Labels) == 1 && b.Labels[0] == fetchLabel {
				if attr, ok := b.Body.Attributes[name]; ok {
					return attr.SrcRange
				}
				return b.DefRange()
			}
		}
	}
	return block.DefRange
}
