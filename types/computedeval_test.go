package types

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newFailingEval returns a computedEval whose poll scope cannot be opened, plus
// an observer over its log. The scope error is the simplest of the three
// failure paths to drive and takes the same reporting route as the other two.
func newFailingEval(t *testing.T) (*computedEval, *observer.ObservedLogs, *error) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	scopeErr := errors.New("no backend")
	eval := &computedEval{
		kind:   "gauge",
		expr:   hcl.StaticExpr(cty.NumberIntVal(1), hcl.Range{}),
		logger: zap.New(core),
		scopeFn: func(ctx context.Context) (PollScope, error) {
			if scopeErr != nil {
				return PollScope{}, scopeErr
			}
			return PollScope{Ctx: ctx, EvalCtx: &hcl.EvalContext{}, Done: func(error) {}}, nil
		},
	}
	return eval, logs, &scopeErr
}

// A `value` that cannot be evaluated cannot be evaluated at any poll, so at
// Error level one broken expression restates one fact every interval forever.
// The first time is loud; the repeats are not.
func TestComputedEvalDampensRepeatedFailure(t *testing.T) {
	eval, logs, _ := newFailingEval(t)

	for range 5 {
		eval.evaluate(context.Background())
	}

	errs := logs.FilterLevelExact(zapcore.ErrorLevel).All()
	require.Len(t, errs, 1, "an unchanging failure is one fact, not five")
	assert.Equal(t, "computed gauge: building poll context", errs[0].Message)

	// The quiet lines still say it is still happening, and how long for.
	debugs := logs.FilterLevelExact(zapcore.DebugLevel).All()
	require.Len(t, debugs, 4)
	assert.Equal(t, int64(4), debugs[3].ContextMap()["repeats"])
}

// A failure that changes is news, so it is loud again.
func TestComputedEvalReportsChangedFailure(t *testing.T) {
	eval, logs, scopeErr := newFailingEval(t)

	eval.evaluate(context.Background())
	eval.evaluate(context.Background())
	*scopeErr = errors.New("something else entirely")
	eval.evaluate(context.Background())

	errs := logs.FilterLevelExact(zapcore.ErrorLevel).All()
	require.Len(t, errs, 2)
	assert.Equal(t, "no backend", errs[0].ContextMap()["error"])
	assert.Equal(t, "something else entirely", errs[1].ContextMap()["error"])
}

// Recovery is as worth saying as the break was — and it re-arms the loud path,
// so a failure that comes back is reported rather than swallowed as a repeat.
func TestComputedEvalReportsRecovery(t *testing.T) {
	eval, logs, scopeErr := newFailingEval(t)

	eval.evaluate(context.Background())
	eval.evaluate(context.Background())
	eval.evaluate(context.Background())
	*scopeErr = nil
	eval.evaluate(context.Background())

	recovered := logs.FilterMessage("computed gauge: recovered").All()
	require.Len(t, recovered, 1)
	assert.Equal(t, int64(3), recovered[0].ContextMap()["failed_polls"])

	*scopeErr = errors.New("no backend")
	eval.evaluate(context.Background())
	assert.Len(t, logs.FilterLevelExact(zapcore.ErrorLevel).All(), 2)
}

// Nothing is said about a recovery that follows no failure.
func TestComputedEvalSilentWhenHealthy(t *testing.T) {
	eval, logs, scopeErr := newFailingEval(t)
	*scopeErr = nil

	eval.evaluate(context.Background())
	eval.evaluate(context.Background())

	assert.Empty(t, logs.All())
}
