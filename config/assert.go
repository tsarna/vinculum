package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"go.uber.org/zap"
)

// The assert block is used to assert a condition at configuration processing runtime.
// It is mainly intended for testing.
type Assert struct {
	Name      string `hcl:"name,label"`
	Condition bool   `hcl:"condition"`
}

type AssertBlockHandler struct {
	BlockHandlerBase
}

func NewAssertBlockHandler() *AssertBlockHandler {
	return &AssertBlockHandler{}
}

func (h *AssertBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "assert." + block.Labels[0], nil
}

func (h *AssertBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	assertion := Assert{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &assertion)
	if diags.HasErrors() {
		return diags
	}

	// Manually set the name from the block label since DecodeBody doesn't handle labels
	if len(block.Labels) > 0 {
		assertion.Name = block.Labels[0]
	}

	if !assertion.Condition {
		config.Logger.Error("Assertion failed", zap.String("assert", assertion.Name), zap.Any("location", block.DefRange))

		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Assertion failed",
				Detail:   fmt.Sprintf("Assertion %s failed", assertion.Name),
				Subject:  &block.DefRange,
			},
		}
	}

	return nil
}
