// Package telemetry encapsulates OpenTelemetry SDK boilerplate for the
// workshop application. It provides entry points to create a
// trace.Tracer and a metric.Meter from an OTLP/HTTP endpoint URL,
// falling back to noop implementations when no endpoint is configured.
package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const serviceName = "workshop"

// NewTracer creates a tracer. If endpoint is empty, returns a noop tracer
// and a no-op shutdown function. Otherwise, creates an OTLP/HTTP exporter,
// a batch span processor, a TracerProvider with resource attributes, and a
// tracer scoped to "github.com/andrewhowdencom/workshop".
func NewTracer(endpoint string) (trace.Tracer, func(context.Context) error, error) {
	if endpoint == "" {
		return noop.NewTracerProvider().Tracer(""),
			func(context.Context) error { return nil },
			nil
	}

	if _, err := url.Parse(endpoint); err != nil {
		return nil, nil, fmt.Errorf("invalid tracing endpoint %q: %w", endpoint, err)
	}

	ctx := context.Background()

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	processor := sdktrace.NewBatchSpanProcessor(exporter)

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create OTel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
		sdktrace.WithResource(res),
	)

	tracer := provider.Tracer("github.com/andrewhowdencom/workshop")

	shutdown := func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return provider.Shutdown(shutdownCtx)
	}

	return tracer, shutdown, nil
}

// NewMeter creates a meter. If endpoint is empty, returns a noop meter
// and a no-op shutdown function. Otherwise, creates an OTLP/HTTP metric
// exporter, a periodic reader, a MeterProvider with resource attributes,
// and a meter scoped to "github.com/andrewhowdencom/workshop".
func NewMeter(endpoint string) (metric.Meter, func(context.Context) error, error) {
	if endpoint == "" {
		return metricnoop.NewMeterProvider().Meter(""),
			func(context.Context) error { return nil },
			nil
	}

	if _, err := url.Parse(endpoint); err != nil {
		return nil, nil, fmt.Errorf("invalid metrics endpoint %q: %w", endpoint, err)
	}

	ctx := context.Background()

	exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(exporter)

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create OTel resource: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)

	meter := provider.Meter("github.com/andrewhowdencom/workshop")

	shutdown := func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return provider.Shutdown(shutdownCtx)
	}

	return meter, shutdown, nil
}
