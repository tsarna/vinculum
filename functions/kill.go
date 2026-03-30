package functions

import (
	"fmt"
	"syscall"

	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("kill", func(c *cfg.Config) map[string]function.Function {
		if c.GetFeature("allowkill") == "" {
			return nil
		}
		return map[string]function.Function{
			"kill": KillFunc,
		}
	})
}

// KillFunc sends a signal to a process. Both pid and signal are integers.
// Returns true on success, or an error if the syscall fails.
var KillFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "pid", Type: cty.Number},
		{Name: "signal", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		pid, accuracy := args[0].AsBigFloat().Int64()
		if accuracy != 0 {
			return cty.False, fmt.Errorf("pid must be an integer")
		}
		sig, accuracy := args[1].AsBigFloat().Int64()
		if accuracy != 0 {
			return cty.False, fmt.Errorf("signal must be an integer")
		}
		if err := syscall.Kill(int(pid), syscall.Signal(sig)); err != nil {
			return cty.False, fmt.Errorf("kill(%d, %d): %w", pid, sig, err)
		}
		return cty.True, nil
	},
})
