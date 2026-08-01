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

// Schema describes the assert block for `vinculum schema`.
func (h *AssertBlockHandler) Schema() TypeSchema { return assertSchema }

var assertSchema = TypeSchema{
	Sample:  &Assert{},
	Summary: "Aborts startup unless a condition holds.",
	Doc: `Checks that ` + "`condition`" + ` is true while the configuration is processed, and
aborts startup if it is not. The block's name label appears in the error message.

Primarily intended for test cases, but also useful for validating that required
environment variables are set and sensible.`,
	Attrs: map[string]AttrMeta{
		"condition": {
			Summary: "Expression that must evaluate to true.",
			Doc:     "Evaluated once, while the configuration is loaded — not at runtime.",
			Hint:    HintExpression,
		},
	},
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
		config.UserLogger.Error("Assertion failed", zap.String("assert", assertion.Name), zap.Any("location", block.DefRange))

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
