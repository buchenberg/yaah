package yaah

import (
	"context"
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/observability"
)

// initOtel initialises OpenTelemetry when configured. Serve mode injects
// extraOtelProcessors (an in-memory BufferingSpanProcessor) and sets
// otelInMemoryOnly so tracing activates without an OTLP endpoint.
func initOtel(cfg *config.Config, skipOtel bool) (func(context.Context) error, bool, error) {
	noop := func(_ context.Context) error { return nil }
	if skipOtel || (!cfg.Observability.Otel.Enabled && len(extraOtelProcessors) == 0) {
		return noop, false, nil
	}
	otelCfg := observability.Config{
		Enabled:         true,
		Endpoint:        cfg.Observability.Otel.Endpoint,
		ServiceName:     cfg.Observability.Otel.ServiceName,
		Traces:          true,
		Metrics:         cfg.Observability.Otel.Metrics,
		ExtraProcessors: extraOtelProcessors,
	}
	if otelInMemoryOnly {
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
	if os.Getenv("YAAH_OTEL_ENABLED") == "true" {
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
