// Package observability provides OpenTelemetry tracing and metrics
// for the yaah agent harness.
package observability

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds OpenTelemetry initialization settings.
type Config struct {
	Enabled     bool
	Endpoint    string // OTLP gRPC endpoint (e.g. "localhost:4317")
	ServiceName string
	Traces      bool
	Metrics     bool

	// ExtraProcessors are additional span processors attached to the
	// TracerProvider alongside the OTLP batcher. Used by serve mode to
	// capture spans in-memory (BufferingSpanProcessor) for programmatic
	// trace querying without an external backend. When Endpoint is empty,
	// the OTLP exporter is skipped entirely and only these processors
	// receive spans.
	ExtraProcessors []sdktrace.SpanProcessor
}

// DefaultConfig returns sensible defaults for local development
// (Jaeger all-in-one on the default OTLP port).
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Endpoint:    "localhost:4317",
		ServiceName: "yaah",
		Traces:      true,
		Metrics:     true,
	}
}

// tracer is the package-level tracer shared by middleware and wrappers.
var tracer = otel.Tracer("yaah")

// Setup initialises the global OpenTelemetry TracerProvider and
// MeterProvider. Callers must defer the returned Shutdown.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if !cfg.Enabled {
		return func(_ context.Context) error { return nil }, nil
	}

	res, err := sdkresource.New(ctx,
		sdkresource.WithFromEnv(),
		sdkresource.WithTelemetrySDK(),
		sdkresource.WithHost(),
		sdkresource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion("0.11.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	var shutdowns []func(context.Context) error

	if cfg.Traces {
		tpOpts := []sdktrace.TracerProviderOption{
			sdktrace.WithResource(res),
		}

		// Only create the OTLP exporter when an endpoint is configured.
		// Serve mode leaves Endpoint empty so spans flow solely to the
		// in-memory ExtraProcessors without a network dependency.
		if cfg.Endpoint != "" {
			exp, err := otlptracehttp.New(ctx,
				otlptracehttp.WithEndpoint(cfg.Endpoint),
				otlptracehttp.WithInsecure(),
			)
			if err != nil {
				return nil, fmt.Errorf("otel trace exporter: %w", err)
			}
			tpOpts = append(tpOpts, sdktrace.WithBatcher(exp))
		}

		for _, p := range cfg.ExtraProcessors {
			tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(p))
		}

		tp := sdktrace.NewTracerProvider(tpOpts...)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	if cfg.Metrics && cfg.Endpoint != "" {
		exp, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpoint(cfg.Endpoint),
			otlpmetrichttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("otel metric exporter: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
				sdkmetric.WithInterval(15*time.Second),
			)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		for _, s := range shutdowns {
			if err := s(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}
