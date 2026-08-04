package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/internal/schemadoc"
)

// `vinculum schema --format markdown`: the same description, rendered as
// documentation rather than as JSON.
//
// Three modes. With neither --update nor --check it writes the whole language
// as one document. With --update it rewrites the marked regions of the pages
// named, leaving everything else byte for byte. With --check it reports
// whether those regions are current and writes nothing, which is what CI runs
// alongside `--strict --require-docs`: together they close both directions,
// since one fails on an attribute that is undocumented and the other on
// documentation that has not been regenerated.

func runSchemaMarkdown(cmd *cobra.Command, doc *config.SchemaDocument) error {
	switch {
	case len(schemaUpdate) > 0:
		return updateMarkdown(cmd, doc, schemaUpdate, false)
	case len(schemaCheck) > 0:
		return updateMarkdown(cmd, doc, schemaCheck, true)
	}

	text := schemadoc.RenderMarkdown(
		schemadoc.Everything(doc, schemadoc.WalkOptions{}),
		schemadoc.MarkdownOptions{})

	if schemaOutput != "" {
		if err := os.WriteFile(schemaOutput, []byte(text), 0o644); err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("failed to write: %w", err)}
		}
		return nil
	}
	_, err := cmd.OutOrStdout().Write([]byte(text))
	if err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("failed to write: %w", err)}
	}
	return nil
}

// updateMarkdown rewrites — or, when checking, compares — the marked regions
// of every page named.
func updateMarkdown(cmd *cobra.Command, doc *config.SchemaDocument, paths []string, checkOnly bool) error {
	files, err := markdownFiles(paths)
	if err != nil {
		return &ExitCodeError{Code: 2, Err: err}
	}

	var stale []string
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("%s: %w", path, err)}
		}

		updated, changed, err := schemadoc.UpdateRegions(doc, string(src))
		if err != nil {
			// A malformed marker is a usage error in the document, not a
			// failure of the generation: say which file, and stop.
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("%s: %w", path, err)}
		}
		if len(changed) == 0 {
			continue
		}

		for _, region := range changed {
			stale = append(stale, fmt.Sprintf("%s: %s", path, region))
		}
		if checkOnly {
			continue
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("%s: %w", path, err)}
		}
	}

	if len(stale) == 0 {
		return nil
	}

	verb := "updated"
	if checkOnly {
		verb = "out of date"
	}
	for _, s := range stale {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", verb, s)
	}
	if !checkOnly {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"\nRun `vinculum schema --format markdown --update %s` to regenerate.\n",
		strings.Join(paths, " "))
	return &ExitCodeError{
		Code:     1,
		Err:      fmt.Errorf("%d generated region(s) out of date", len(stale)),
		Reported: true,
	}
}

// markdownFiles expands the given paths: a directory contributes every .md
// file under it, a file contributes itself.
func markdownFiles(paths []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			continue
		}
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".md") || seen[p] {
				return nil
			}
			seen[p] = true
			out = append(out, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(out)
	return out, nil
}
