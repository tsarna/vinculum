package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"go.uber.org/zap"
)

// gitDir is the repository metadata directory, never copied into a destination.
const gitDir = ".git"

// materializeFetches resolves each fetch's `from` subtree against the cloned
// working tree and copies it into the fetch's `into` destination, applying the
// destination ownership rules. The first failure aborts.
func materializeFetches(def *GitDefinition, workdir string, logger *zap.Logger) hcl.Diagnostics {
	for i := range def.Fetches {
		f := &def.Fetches[i]
		if diags := materializeOne(def, f, workdir, logger); diags.HasErrors() {
			return diags
		}
	}
	return nil
}

// materializeOne copies a single fetch's subtree to its destination.
func materializeOne(def *GitDefinition, f *GitFetch, workdir string, logger *zap.Logger) hcl.Diagnostics {
	from := f.From
	if from == "" {
		from = "."
	}
	srcPath := filepath.Join(workdir, filepath.Clean(from))

	// Defensive re-check that the resolved path stays inside the clone (the
	// `from` value was validated at parse time, but guard against surprises).
	if rel, err := filepath.Rel(workdir, srcPath); err != nil ||
		rel == ".." || filepath.IsAbs(rel) || hasDotDotPrefix(rel) {
		return fetchDiag(def, f, "Invalid git fetch from path",
			fmt.Sprintf("Fetch %q resolves outside the repository.", f.Name))
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return fetchDiag(def, f, "git fetch source missing",
			fmt.Sprintf("Fetch %q: path %q does not exist in the repository.", f.Name, from))
	}

	if diags := prepareDest(def, f); diags.HasErrors() {
		return diags
	}

	if info.IsDir() {
		if err := copyTree(srcPath, f.Into, logger); err != nil {
			return fetchDiag(def, f, "git fetch copy failed",
				fmt.Sprintf("Fetch %q: copying %q into %q: %s", f.Name, from, f.Into, err))
		}
	} else {
		// A single file is copied into the destination directory.
		dst := filepath.Join(f.Into, filepath.Base(srcPath))
		if err := copyFile(srcPath, dst, info.Mode()); err != nil {
			return fetchDiag(def, f, "git fetch copy failed",
				fmt.Sprintf("Fetch %q: copying file %q into %q: %s", f.Name, from, f.Into, err))
		}
	}

	if logger != nil {
		logger.Info("git fetch materialized",
			zap.String("label", def.Label),
			zap.String("fetch", f.Name),
			zap.String("from", from),
			zap.String("into", f.Into))
	}
	return nil
}

// prepareDest enforces the destination ownership model and leaves `into` as an
// existing, ready-to-populate directory:
//
//  1. into absent          -> create it.
//  2. into exists & empty   -> use as-is.
//  3. into exists & non-empty -> fatal, unless overwrite=true clears it first.
func prepareDest(def *GitDefinition, f *GitFetch) hcl.Diagnostics {
	info, err := os.Stat(f.Into)
	switch {
	case os.IsNotExist(err):
		if mkErr := os.MkdirAll(f.Into, 0o755); mkErr != nil {
			return fetchDiag(def, f, "git fetch destination error",
				fmt.Sprintf("Fetch %q: creating %q: %s", f.Name, f.Into, mkErr))
		}
		return nil
	case err != nil:
		return fetchDiag(def, f, "git fetch destination error",
			fmt.Sprintf("Fetch %q: inspecting %q: %s", f.Name, f.Into, err))
	case !info.IsDir():
		return fetchDiag(def, f, "git fetch destination is not a directory",
			fmt.Sprintf("Fetch %q: destination %q exists and is not a directory.", f.Name, f.Into))
	}

	empty, err := dirIsEmpty(f.Into)
	if err != nil {
		return fetchDiag(def, f, "git fetch destination error",
			fmt.Sprintf("Fetch %q: reading %q: %s", f.Name, f.Into, err))
	}
	if empty {
		return nil
	}

	if !f.Overwrite {
		return fetchDiag(def, f, "git fetch destination not empty",
			fmt.Sprintf("Fetch %q: destination %q exists and is not empty. It holds data "+
				"Vinculum did not write; set `overwrite = true` to let this fetch take ownership.",
				f.Name, f.Into))
	}

	if err := clearDirContents(f.Into); err != nil {
		return fetchDiag(def, f, "git fetch destination error",
			fmt.Sprintf("Fetch %q: clearing %q: %s", f.Name, f.Into, err))
	}
	return nil
}

// copyTree copies the contents of src (a directory) into dst, recreating the
// directory structure. The repository's .git metadata is never copied.
// Symlinks are skipped (with a debug log) to avoid materializing links that
// could point outside the destination.
func copyTree(src, dst string, logger *zap.Logger) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		// Skip the repository metadata directory anywhere in the tree.
		if d.IsDir() && d.Name() == gitDir {
			return filepath.SkipDir
		}

		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&os.ModeSymlink != 0:
			if logger != nil {
				logger.Debug("git fetch skipping symlink", zap.String("path", rel))
			}
			return nil
		case d.Type().IsRegular():
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			return copyFile(path, target, info.Mode().Perm())
		default:
			// Skip devices, sockets, pipes, etc.
			if logger != nil {
				logger.Debug("git fetch skipping non-regular file", zap.String("path", rel))
			}
			return nil
		}
	})
}

// copyFile copies a single regular file, creating parent directories as needed
// and preserving the given mode.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirIsEmpty reports whether dir contains no entries.
func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// clearDirContents removes everything inside dir but keeps dir itself (so a
// pre-created mount point or volume is not unmounted).
func clearDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// hasDotDotPrefix reports whether a cleaned relative path begins with a parent
// (`..`) component.
func hasDotDotPrefix(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}

// fetchDiag builds a fatal diagnostic for a fetch, with the Subject pointing at
// the fetch's definition range.
func fetchDiag(def *GitDefinition, f *GitFetch, summary, detail string) hcl.Diagnostics {
	r := f.DefRange
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  &r,
	}}
}
