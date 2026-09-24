package obs

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// InitTracing installs an OpenTelemetry tracer provider whose spans are written
// to the logger. It returns a shutdown function. A real OTLP exporter can be
// substituted here without touching call sites.
func InitTracing(service string, logger *slog.Logger) func(context.Context) error {
	exp := &logExporter{logger: logger}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
	)
	otel.SetTracerProvider(tp)
	_ = service
	return tp.Shutdown
}

// Tracer returns the conclave-core tracer.
func Tracer() trace.Tracer { return otel.Tracer("github.com/texhik/conclave/conclave-core") }

type logExporter struct {
	logger *slog.Logger
}

func (e *logExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, s := range spans {
		attrs := make([]any, 0, len(s.Attributes())*2)
		for _, a := range s.Attributes() {
			attrs = append(attrs, string(a.Key), a.Value.AsString())
		}
		e.logger.Log(ctx, slog.LevelDebug, "span",
			append([]any{
				"name", s.Name(),
				"trace_id", s.SpanContext().TraceID().String(),
				"duration_ms", s.EndTime().Sub(s.StartTime()).Milliseconds(),
				"status", s.Status().Code.String(),
			}, attrs...)...)
	}
	return nil
}

func (e *logExporter) Shutdown(context.Context) error { return nil }

// Span starts a span and returns the updated context and an end function.
func Span(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}
