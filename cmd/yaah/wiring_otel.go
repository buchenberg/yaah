package yaah

import (
	"context"
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/observability"
)

// initOtel initialises OpenTelemetry when configured. Serve mode passes
// extra processors (an in-memory BufferingSpanProcessor) and the
// in-memory-only mode via opts so tracing activates without an OTLP
// endpoint.
func initOtel(cfg *config.Config, opts SessionOptions, skipOtel bool) (func(context.Context) error, bool, error) {
	noop := func(_ context.Context) error { return nil }
	enabledByEnv := os.Getenv("YAAH_OTEL_ENABLED") == "true"
	if skipOtel || (!cfg.Observability.Otel.Enabled && len(opts.OtelProcessors) == 0 && !enabledByEnv) {
		return noop, false, nil
	}
	otelCfg := observability.Config{
		Enabled:         true,
		Endpoint:        cfg.Observability.Otel.Endpoint,
		ServiceName:     cfg.Observability.Otel.ServiceName,
		ServiceVersion:  version,
		Traces:          true,
		Metrics:         cfg.Observability.Otel.Metrics,
		ExtraProcessors: opts.OtelProcessors,
	}
	if opts.OtelInMemoryOnly {
		otelCfg.Endpoint = ""
		otelCfg.Metrics = false
	} else {
		if otelCfg.Endpoint == "" {
			otelCfg.Endpoint = "localhost:4317"
		}
		if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
			otelCfg.Endpoint = ep
		}
	}
	if enabledByEnv {
		otelCfg.Enabled = true
	}
	if otelCfg.ServiceName == "" {
		otelCfg.ServiceName = "yaah"
	}
	sd, err := observability.Setup(context.Background(), otelCfg)
	if err != nil {
		return nil, false, fmt.Errorf("otel: %w", err)
	}
	return sd, true, nil
}

// wrapProviderWithOtel wraps the provider with OTel instrumentation when
// tracing is active. Non-streaming providers are returned unchanged since
// the InstrumentedProvider requires streaming for token-level spans.
func wrapProviderWithOtel(provider agent.Provider, otelActive bool, verbose bool) agent.Provider {
	if !otelActive {
		return provider
	}
	if sp, ok := provider.(agent.StreamProvider); ok {
		return &observability.InstrumentedProvider{Inner: sp, Verbose: verbose}
	}
	return provider
}
