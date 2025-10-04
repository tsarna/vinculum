package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/heimdalr/dag"
)

func ExtractReferencesFromAttribute(attr *hcl.Attribute) []string {
	var refs []string

	// Get all variables referenced in the expression
	for _, traversal := range attr.Expr.Variables() {
		if len(traversal) > 0 {
			// Convert traversal to string reference
			ref := traversal.RootName()
			for _, step := range traversal[1:] {
				if attr, ok := step.(hcl.TraverseAttr); ok {
					ref += "." + attr.Name
				}
			}
			refs = append(refs, ref)
		}
	}

	return refs
}

func ExtractReferencesFromBlock(block *hcl.Block) []string {
	var refs []string

	// Extract references from the block body
	refs = append(refs, ExtractReferencesFromBody(block.Body)...)

	return refs
}

func ExtractReferencesFromBody(body hcl.Body) []string {
	var refs []string

	// Get partial content to access attributes and blocks
	content, _, _ := body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{},
		Blocks:     []hcl.BlockHeaderSchema{},
	})

	// Extract references from all attributes
	for _, attr := range content.Attributes {
		refs = append(refs, ExtractReferencesFromAttribute(attr)...)
	}

	// Recursively extract references from nested blocks
	for _, nestedBlock := range content.Blocks {
		refs = append(refs, ExtractReferencesFromBlock(nestedBlock)...)
	}

	return refs
}

// SortAttributesByDependencies returns attribute names sorted in dependency order
func SortAttributesByDependencies(attrs hcl.Attributes) ([]*hcl.Attribute, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	// Create DAG
	graph := dag.NewDAG()

	for _, attr := range attrs {
		err := graph.AddVertexByID(attr.Name, attr)
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to add attribute to dependency graph",
				Detail:   fmt.Sprintf("Error adding attribute %s: %s", attr.Name, err),
				Subject:  &attr.NameRange,
			})
		}
	}

	// Add edges for dependencies
	for name, attr := range attrs {
		refs := ExtractReferencesFromAttribute(attr)

		for _, ref := range refs {
			if _, exists := attrs[ref]; exists {
				err := graph.AddEdge(ref, name)
				if err != nil {
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Circular dependency detected",
						Detail:   fmt.Sprintf("Cannot add dependency from %s to %s: %s", ref, name, err),
						Subject:  &attr.Range,
					})
				}
			} else {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Dependency not found",
					Detail:   fmt.Sprintf("Dependency %s of %s not found", ref, name),
					Subject:  &attr.Range,
				})
			}
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}

	// Get topological ordering
	visitor := &attributeVertexVisitor{}
	graph.OrderedWalk(visitor)

	return visitor.attrs, diags
}

type attributeVertexVisitor struct {
	attrs []*hcl.Attribute
}

func (v *attributeVertexVisitor) Visit(vertex dag.Vertexer) {
	_, value := vertex.Vertex()
	v.attrs = append(v.attrs, value.(*hcl.Attribute))
}

func (cb *ConfigBuilder) SortBlocksByDependencies(blocks hcl.Blocks) (hcl.Blocks, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	graph := dag.NewDAG()

	for _, block := range blocks {
		if handler, ok := cb.blockHandlers[block.Type]; ok {
			id, depDiags := handler.GetBlockDependencyId(block)
			diags = diags.Extend(depDiags)

			if id == "" {
				continue
			}

			err := graph.AddVertexByID(id, block)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to add block to dependency graph",
					Detail:   fmt.Sprintf("Error adding block %s: %s", block.Type, err),
					Subject:  &block.TypeRange,
				})
			}
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}

	/*
	   for _, block := range blocks {
	   		if handler, ok := BlockHandlers[block.Type]; ok {
	   			ids, depDiags := handler.GetBlockDependencies(block)

	   			for _, id := range ids {
	   				err := graph.AddEdge(id, block.Type)
	   				if err != nil {
	   					diags = diags.Append(&hcl.Diagnostic{
	   						Severity: hcl.DiagError,
	   						Summary:  "Failed to add edge to dependency graph",

	   			}
	   		}
	   	}
	*/

	if diags.HasErrors() {
		return nil, diags
	}

	return blocks, diags
}
