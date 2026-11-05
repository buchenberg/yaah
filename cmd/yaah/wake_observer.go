package yaah

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/wake"
)

// wakeObserver turns the engine's lifecycle notifications into OTel spans and
// hook events. Only the predicate kind is recorded: the target (a URL, a
// command, a file path) must never reach a span attribute or a hook log.
type wakeObserver struct {
	hooks *events.HookEmitter
}

func newWakeObserver(hooks *events.HookEmitter) *wakeObserver {
	return &wakeObserver{hooks: hooks}
}

func wakeKindOf(spec wake.Spec) string { return string(spec.SourceOf()) }

func (o *wakeObserver) WaitStarted(ctx context.Context, spec wake.Spec, timeout time.Duration) context.Context {
	kind := wakeKindOf(spec)
	o.hooks.Emit(events.HookEvent{
		Event:      events.WaitStart,
		WakeKind:   kind,
		DurationMs: timeout.Milliseconds(),
	})
	ctx, _ = observability.StartWait(ctx, kind)
	return ctx
}

func (o *wakeObserver) WaitFinished(ctx context.Context, spec wake.Spec, fired, timedOut, interrupted, errored bool, elapsed time.Duration) {
	state := "completed"
	switch {
	case errored:
		state = "errored"
	case fired:
		state = "fired"
	case timedOut:
		state = "timed_out"
	case interrupted:
		state = "interrupted"
	}
	o.hooks.Emit(events.HookEvent{
		Event:      events.WaitEnd,
		WakeKind:   wakeKindOf(spec),
		WakeState:  state,
		DurationMs: elapsed.Milliseconds(),
	})
	observability.FinishWait(trace.SpanFromContext(ctx), fired, timedOut, interrupted, elapsed)
}

func (o *wakeObserver) WatchArmed(info wake.WatchInfo) {
	kind := wakeKindOf(wake.Spec{On: info.On})
	o.hooks.Emit(events.HookEvent{
		Event:     events.WatchArm,
		WakeKind:  kind,
		WakeID:    info.ID,
		WakeState: "armed",
	})
	observability.RecordWatchArm(context.Background(), kind, info.ID)
}

func (o *wakeObserver) WatchFired(info wake.WatchInfo, missed bool) {
	state := "fired"
	if info.Evidence != nil && info.Evidence.Kind == "watch" && info.Evidence.Status != "" {
		state = info.Evidence.Status
	}
	kind := wakeKindOf(wake.Spec{On: info.On})
	o.hooks.Emit(events.HookEvent{
		Event:      events.WatchFire,
		WakeKind:   kind,
		WakeID:     info.ID,
		WakeState:  state,
		WakeMissed: missed,
	})
	observability.RecordWatchFire(context.Background(), kind, state, info.ID, missed)
}

func (o *wakeObserver) WatchCancelled(id string) {
	o.hooks.Emit(events.HookEvent{
		Event:     events.WatchCancel,
		WakeID:    id,
		WakeState: "cancelled",
	})
}
