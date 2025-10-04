package config

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
)

func (cb *ConfigBuilder) GetBlocks(bodies []hcl.Body) (hcl.Blocks, hcl.Diagnostics) {
	diags := hcl.Diagnostics{}

	var blocks hcl.Blocks

	for _, body := range bodies {
		content, _, partialDiags := body.PartialContent(configSchema)
		diags = diags.Extend(partialDiags)

		blocks = append(blocks, content.Blocks...)
	}

	return blocks, diags
}

func ParseConfigFiles(sources ...any) ([]hcl.Body, hcl.Diagnostics) {
	parser := hclparse.NewParser()
	var diags hcl.Diagnostics
	bodies := make([]hcl.Body, 0)

	for _, source := range sources {
		switch v := source.(type) {
		case string:
			// Check if v is a file or directory
			info, err := os.Stat(v)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to stat file",
					Detail:   fmt.Sprintf("Error statting %s: %s", v, err),
				})
			}

			if info.IsDir() {
				newBodies, newDiags := parseDirectory(parser, v)
				diags = diags.Extend(newDiags)
				if diags.HasErrors() {
					return nil, diags
				}
				bodies = append(bodies, newBodies...)
			} else {
				file, parseDiags := parser.ParseHCLFile(v)
				diags = diags.Extend(parseDiags)
				bodies = append(bodies, file.Body)
			}
		case []byte:
			filename := fmt.Sprintf("<bytes@%p>", v)
			body, parseDiags := parser.ParseHCL(v, filename)
			diags = diags.Extend(parseDiags)
			bodies = append(bodies, body.Body)
		case embed.FS:
			newBodies, newDiags := parseFS(parser, v)
			diags = diags.Extend(newDiags)
			if diags.HasErrors() {
				return nil, diags
			}
			bodies = append(bodies, newBodies...)
		case []string:
			for _, file := range v {
				_, parseDiags := parser.ParseHCLFile(file)
				diags = diags.Extend(parseDiags)
			}
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid source type",
				Detail:   fmt.Sprintf("Invalid source type: %T", v),
			})
		}
	}

	return bodies, diags
}

func parseDirectory(parser *hclparse.Parser, dir string) ([]hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	bodies := make([]hcl.Body, 0)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to access file or directory",
				Detail:   fmt.Sprintf("Error accessing %s: %s", path, err),
			})
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".vcl") {
			file, parseDiags := parser.ParseHCLFile(path)
			diags = diags.Extend(parseDiags)
			bodies = append(bodies, file.Body)
		}

		return nil
	})

	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to walk directory",
			Detail:   fmt.Sprintf("Error walking directory %s: %s", dir, err),
		})
	}

	return bodies, diags
}

func parseFS(parser *hclparse.Parser, embedFS embed.FS) ([]hcl.Body, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	bodies := make([]hcl.Body, 0)

	err := fs.WalkDir(embedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to access file or directory",
				Detail:   fmt.Sprintf("Error accessing %s: %s", path, err),
			})
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".vcl") {
			content, err := fs.ReadFile(embedFS, path)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to read file",
					Detail:   fmt.Sprintf("Error reading %s: %s", path, err),
				})
			}

			file, parseDiags := parser.ParseHCL(content, path)
			diags = diags.Extend(parseDiags)
			bodies = append(bodies, file.Body)
		}
		return nil
	})

	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to walk directory",
			Detail:   fmt.Sprintf("Error walking directory %v: %s", embedFS, err),
		})
	}

	return bodies, diags
}
