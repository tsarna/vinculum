package functions

import (
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("llm", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"llm::wrap": LLMWrapFunc,
		}
	})
}

// LLMWrapFunc wraps user-controlled content in <user_input> XML-like delimiters
// as a prompt injection mitigation. The system prompt should reference these tags
// to tell the model where untrusted input begins and ends.
//
// Input: "hello"
// Output: "<user_input>\nhello\n</user_input>"
var LLMWrapFunc = function.New(&function.Spec{
	Description: "Wraps untrusted content in <user_input> delimiters as a prompt-injection mitigation; returns the wrapped string",
	Params: []function.Parameter{
		{Name: "content", Type: cty.String, Description: "The user-controlled content to wrap"},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		content := args[0].AsString()
		return cty.StringVal("<user_input>\n" + content + "\n</user_input>"), nil
	},
})
