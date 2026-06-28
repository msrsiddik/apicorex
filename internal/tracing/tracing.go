// Package tracing wires OpenTelemetry distributed tracing. It is optional:
// if OTEL_EXPORTER_OTLP_ENDPOINT is unset, tracing is a no-op (zero overhead).
package tracing

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceName = "apicorex-core"

// Tracer is the global tracer used by the dispatcher.
func Tracer() trace.Tracer { return otel.Tracer(serviceName) }

// Init sets up the OTLP exporter + tracer provider if OTEL_EXPORTER_OTLP_ENDPOINT
// is set. Returns a shutdown func (no-op when tracing is disabled).
func Init(ctx context.Context) (func(context.Context) error, bool, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// tracing disabled — install a no-op propagator so context plumbing still works
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func(context.Context) error { return nil }, false, nil
	}

	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	exporter, err := otlptracehttp.New(initCtx) // reads OTEL_EXPORTER_OTLP_ENDPOINT
	if err != nil {
		return nil, false, err
	}

	res, err := resource.New(initCtx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, false, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, true, nil
}

// PluginAttr is a convenience for tagging spans with the plugin name.
func PluginAttr(name string) attribute.KeyValue { return attribute.String("plugin", name) }
