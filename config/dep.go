package config

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
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
	return ExtractReferencesFromBody(block.Body, nil)
}

// ExtractReferencesFromBody extracts all variable references from an HCL body,
// excluding attributes listed in excludeAttrs. The exclusion applies at all
// levels (top-level and nested blocks). Returns nil for non-hclsyntax bodies.
func ExtractReferencesFromBody(body hcl.Body, excludeAttrs map[string]bool) []string {
	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return nil
	}

	var refs []string

	for name, attr := range syntaxBody.Attributes {
		if excludeAttrs[name] {
			continue
		}
		for _, traversal := range attr.Expr.Variables() {
			if len(traversal) > 0 {
				ref := traversal.RootName()
				for _, step := range traversal[1:] {
					if ta, ok := step.(hcl.TraverseAttr); ok {
						ref += "." + ta.Name
					}
				}
				refs = append(refs, ref)
			}
		}
	}

	for _, block := range syntaxBody.Blocks {
		refs = append(refs, ExtractReferencesFromBody(block.Body, excludeAttrs)...)
	}

	return refs
}

// ExtractBlockDependencies extracts config-time variable references from a block,
// excluding the named runtime-only attributes at all levels.
func ExtractBlockDependencies(block *hcl.Block, excludeAttrs ...string) []string {
	excludeSet := make(map[string]bool, len(excludeAttrs))
	for _, a := range excludeAttrs {
		excludeSet[a] = true
	}
	return ExtractReferencesFromBody(block.Body, excludeSet)
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

// SortBlocksByDependencies returns blocks sorted in dependency order using
// Kahn's algorithm. When multiple blocks are available (no ordering constraint
// between them), they are emitted in their original source order. Blocks without
// a dependency ID (const, signals, function) are appended at the end unchanged.
func (cb *ConfigBuilder) SortBlocksByDependencies(blocks hcl.Blocks) (hcl.Blocks, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	type entry struct {
		id     string
		srcIdx int // position in original blocks slice
		block  *hcl.Block
	}

	var dagEntries []entry   // blocks that participate in ordering
	var otherBlocks hcl.Blocks
	idToIdx := map[string]int{} // block ID → index in dagEntries

	for i, block := range blocks {
		handler, ok := cb.blockHandlers[block.Type]
		if !ok {
			otherBlocks = append(otherBlocks, block)
			continue
		}

		id, depDiags := handler.GetBlockDependencyId(block)
		diags = diags.Extend(depDiags)

		if id == "" {
			otherBlocks = append(otherBlocks, block)
			continue
		}

		idToIdx[id] = len(dagEntries)
		dagEntries = append(dagEntries, entry{id: id, srcIdx: i, block: block})
	}

	if diags.HasErrors() {
		return nil, diags
	}

	n := len(dagEntries)
	inDegree := make([]int, n)
	// dependents[i] holds the indices of blocks that depend on dagEntries[i]
	dependents := make([][]int, n)

	for entryIdx, e := range dagEntries {
		handler := cb.blockHandlers[e.block.Type]
		deps, depDiags := handler.GetBlockDependencies(e.block)
		diags = diags.Extend(depDiags)

		for _, dep := range deps {
			depIdx, known := idToIdx[dep]
			if !known {
				// Silently ignore unknown refs (constants, env vars, auto-created resources).
				continue
			}
			dependents[depIdx] = append(dependents[depIdx], entryIdx)
			inDegree[entryIdx]++
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}

	// Kahn's algorithm: always pick the next available block with the earliest
	// source position, ensuring stable output that preserves file order when
	// there is no explicit dependency between blocks.
	available := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			available = append(available, i)
		}
	}
	sort.Slice(available, func(a, b int) bool {
		return dagEntries[available[a]].srcIdx < dagEntries[available[b]].srcIdx
	})

	result := make(hcl.Blocks, 0, n)
	for len(available) > 0 {
		idx := available[0]
		available = available[1:]
		result = append(result, dagEntries[idx].block)

		var newAvail []int
		for _, depIdx := range dependents[idx] {
			inDegree[depIdx]--
			if inDegree[depIdx] == 0 {
				newAvail = append(newAvail, depIdx)
			}
		}
		if len(newAvail) > 0 {
			available = append(available, newAvail...)
			sort.Slice(available, func(a, b int) bool {
				return dagEntries[available[a]].srcIdx < dagEntries[available[b]].srcIdx
			})
		}
	}

	// If not all blocks were emitted, there is a cycle.
	if len(result) != n {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Circular dependency between blocks",
			Detail:   "There is a circular dependency in the block configuration that prevents determining a valid processing order",
		})
		return nil, diags
	}

	// DAG blocks in dependency order, then blocks without dependency IDs.
	return append(result, otherBlocks...), diags
}
