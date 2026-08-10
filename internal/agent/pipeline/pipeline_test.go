package pipeline

import "testing"

func TestPipeline_ShepherdTraceMiddleware_NilWhenNotInPipeline(t *testing.T) {
	p := NewPipeline(&SteerMiddleware{}, &StalenessMiddleware{})
	if mw := p.ShepherdTraceMiddleware(); mw != nil {
		t.Error("should return nil when shepherd_trace is not in pipeline")
	}
}

func TestPipeline_ShepherdTraceMiddleware_NoopNotReturned(t *testing.T) {
	p := NewPipeline(&noopShepherdTraceMiddleware{})
	if mw := p.ShepherdTraceMiddleware(); mw != nil {
		t.Error("should return nil for noop (not a real *ShepherdTraceMiddleware)")
	}
}
