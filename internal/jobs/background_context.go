package jobs

import "context"

type backgroundContextSeederKey struct{}

// WithBackgroundContextSeeder carries a value-preserving seed into a
// background job. A background job deliberately derives cancellation from the
// session root rather than the dispatching call, so ordinary context values do
// not flow through. A caller that owns per-dispatch values (for example the
// conflict tracker's dispatch id and lane) supplies a seed that re-attaches
// them to the job context without importing this package's callers.
func WithBackgroundContextSeeder(ctx context.Context, seed func(context.Context) context.Context) context.Context {
	if seed == nil {
		return ctx
	}
	return context.WithValue(ctx, backgroundContextSeederKey{}, seed)
}

func seedBackgroundContext(callCtx, jobCtx context.Context) context.Context {
	seed, ok := callCtx.Value(backgroundContextSeederKey{}).(func(context.Context) context.Context)
	if !ok || seed == nil {
		return jobCtx
	}
	seeded := seed(jobCtx)
	if seeded == nil {
		return jobCtx
	}
	return seeded
}
