package procedure

import (
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// Scope implements a chained variable scope for procedure execution.
// Variable lookup walks outward through the parent chain.
// Assignment to an existing name in any ancestor updates that ancestor;
// assignment to a new name creates it in the innermost scope.
type Scope struct {
	vars   map[string]cty.Value
	parent *Scope
}

// NewScope creates a new scope with the given parent (may be nil for root).
func NewScope(parent *Scope) *Scope {
	return &Scope{
		vars:   make(map[string]cty.Value),
		parent: parent,
	}
}

// Get looks up a variable by walking the scope chain outward.
func (s *Scope) Get(name string) (cty.Value, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v, true
		}
	}
	return cty.NilVal, false
}

// isDiscard returns true if the name is a discard identifier (starts with "_").
func isDiscard(name string) bool {
	return strings.HasPrefix(name, "_")
}

// Set assigns a value to a variable. If the name already exists in any
// ancestor scope, that ancestor is updated. Otherwise the variable is
// created in this (innermost) scope. Discard names (starting with "_")
// are ignored.
func (s *Scope) Set(name string, val cty.Value) {
	if isDiscard(name) {
		return
	}
	for cur := s; cur != nil; cur = cur.parent {
		if _, ok := cur.vars[name]; ok {
			cur.vars[name] = val
			return
		}
	}
	s.vars[name] = val
}

// ToMap flattens the scope chain into a single map, with inner scopes
// taking precedence over outer ones.
func (s *Scope) ToMap() map[string]cty.Value {
	result := make(map[string]cty.Value)
	s.collectInto(result)
	return result
}

func (s *Scope) collectInto(m map[string]cty.Value) {
	if s.parent != nil {
		s.parent.collectInto(m)
	}
	for k, v := range s.vars {
		m[k] = v
	}
}
