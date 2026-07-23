package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
	"github.com/tsarna/vinculum/config"
	"go.uber.org/zap"
)

var (
	fmtWrite bool
	fmtList  bool
	fmtLang  string
)

var fmtCmd = &cobra.Command{
	Use:   "fmt [FILE|DIR ...]",
	Short: "Format .vcl, .vinit, and .cty source files",
	Long: `Format Vinculum configuration and functy source.

Files are formatted according to their extension: .vcl and .vinit are
reformatted as HCL, and .cty as functy source. A file that does not parse is
reported and left unchanged — formatting never drops or reorders code.

With no paths (or "-") it reads standard input and writes the result to
standard output; use --lang to say which language stdin holds (default vcl).
Given files or directories (walked recursively for .vcl/.vinit/.cty files,
skipping dot-directories), it prints the formatted source, or with -w rewrites
files in place, or with -l lists the files whose formatting differs.

If any .cty file being formatted annotates parameters with a functy type that a
plugin contributes (via RegisterFunctyType), pass --plugin-path so those plugins
are loaded first and the type resolves; otherwise such files are reported as
having an unknown type and left unchanged. Only the plugin bootstrap runs — no
"git" blocks are materialized.

Examples:
  vinculum fmt config.vcl
  vinculum fmt -w ./configs/
  vinculum fmt -l ./configs/
  vinculum fmt --lang cty < snippet.cty
  vinculum fmt --plugin-path /plugins ./configs/`,
	RunE: runFmt,
}

func init() {
	rootCmd.AddCommand(fmtCmd)

	fmtCmd.Flags().BoolVarP(&fmtWrite, "write", "w", false, "rewrite files in place instead of printing to stdout")
	fmtCmd.Flags().BoolVarP(&fmtList, "list", "l", false, "list files whose formatting differs; do not print or rewrite")
	fmtCmd.Flags().StringVarP(&fmtLang, "lang", "t", "vcl", "language of stdin when no paths are given: vcl or cty")
	fmtCmd.Flags().StringVar(&pluginPath, "plugin-path", "", "directory of Go plugin .so files; load them first so plugin-registered functy types resolve in .cty files")
}

func runFmt(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		return fmtStdin(cmd)
	}

	var failed bool

	// When --plugin-path is set, run the plugin-only .vinit bootstrap over the
	// same paths so plugin-registered functy types are known before any .cty is
	// parsed. Git blocks are intentionally not materialized. Failures are
	// surfaced but non-fatal — files that don't need the plugin still format.
	if pluginPath != "" {
		if diags := config.ProcessVinitPlugins(stringSliceToAnySlice(args), pluginPath, zap.NewNop()); diags.HasErrors() {
			printDiags(cmd.ErrOrStderr(), map[string]*hcl.File{}, diags)
			failed = true
		}
	}

	for _, path := range args {
		files, err := gatherFmtFiles(path)
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "vinculum fmt:", err)
			failed = true
			continue
		}
		for _, file := range files {
			if err := fmtFile(cmd, file); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "vinculum fmt:", err)
				failed = true
			}
		}
	}
	if failed {
		return errors.New("fmt failed")
	}
	return nil
}

// fmtStdin formats standard input to standard output, treating it as the
// language named by --lang (stdin has no extension to dispatch on).
func fmtStdin(cmd *cobra.Command) error {
	src, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return err
	}
	name := "<stdin>"
	var out []byte
	var diags hcl.Diagnostics
	switch fmtLang {
	case "cty":
		out, diags = config.FormatFunctySource(src, name)
	case "vcl", "vinit", "hcl":
		out, diags = formatHCL(src, name)
	default:
		return fmt.Errorf("unknown --lang %q (want vcl or cty)", fmtLang)
	}
	if diags.HasErrors() {
		printDiags(cmd.ErrOrStderr(), map[string]*hcl.File{name: {Bytes: src}}, diags)
		return errors.New("fmt failed")
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// fmtFile formats one file by extension. Default prints to stdout, -w rewrites
// in place, -l lists the path if its formatting differs.
func fmtFile(cmd *cobra.Command, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var out []byte
	var diags hcl.Diagnostics
	if strings.HasSuffix(path, functy.Extension) {
		out, diags = config.FormatFunctySource(src, path)
	} else {
		out, diags = formatHCL(src, path)
	}
	if diags.HasErrors() {
		printDiags(cmd.ErrOrStderr(), map[string]*hcl.File{path: {Bytes: src}}, diags)
		return fmt.Errorf("%s: parse errors", path)
	}

	changed := !bytes.Equal(src, out)
	switch {
	case fmtList:
		if changed {
			fmt.Fprintln(cmd.OutOrStdout(), path)
		}
	case fmtWrite:
		if changed {
			mode := os.FileMode(0o644)
			if info, err := os.Stat(path); err == nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(path, out, mode); err != nil {
				return err
			}
		}
	default:
		if _, err := cmd.OutOrStdout().Write(out); err != nil {
			return err
		}
	}
	return nil
}

// formatHCL reformats HCL (.vcl/.vinit) source. It parse-checks first and, on
// any syntax error, returns src unchanged with the diagnostics so a file that
// does not parse is never reformatted (mirroring the functy formatter's
// invariant). hclwrite.Format is purely token-based, so it is lossless.
func formatHCL(src []byte, filename string) ([]byte, hcl.Diagnostics) {
	_, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return src, diags
	}
	return hclwrite.Format(src), nil
}

// gatherFmtFiles returns the formattable files at path: the file itself, or
// every .vcl/.vinit/.cty file under a directory (recursively, skipping
// dot-directories).
func gatherFmtFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if hasFmtExt(p) {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

func hasFmtExt(path string) bool {
	return strings.HasSuffix(path, ".vcl") ||
		strings.HasSuffix(path, ".vinit") ||
		strings.HasSuffix(path, functy.Extension)
}
