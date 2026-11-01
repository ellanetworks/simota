// Package logging writes simota's logs: JSON as Ella Core, Ella SMSC and Ella IMS write theirs, with the trace context
// of the record's context, if any, so that a log line leads to its trace.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// The keys of a record's trace context: the field names that OpenTelemetry gives a non-OTLP log format, as lowercase
// hex, at the top level of a JSON record (OpenTelemetry specification, "Trace Context in non-OTLP Log Formats").
const (
	TraceIDKey    = "trace_id"
	SpanIDKey     = "span_id"
	TraceFlagsKey = "trace_flags"
)

// New is a logger of records of level and above, written to w as JSON: ts, a lowercase level, and durations as "1.5s".
// A record logged with a context that holds a valid span context, such as by Logger.InfoContext, carries its trace
// context.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewTraceHandler(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch {
			case a.Key == slog.TimeKey:
				a.Key = "ts"
			case a.Key == slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			case a.Value.Kind() == slog.KindDuration:
				a.Value = slog.StringValue(a.Value.Duration().String())
			}

			return a
		},
	})))
}

// TraceHandler adds the trace context of a record's context to the record, at the top level whatever the groups of
// the logger, and passes the record on to the handler it wraps.
type TraceHandler struct {
	// root is the wrapped handler, and handler root with the logger's attributes and groups, which replay adds to a
	// handler. grouped is whether the logger has a group.
	root    slog.Handler
	handler slog.Handler
	replay  func(slog.Handler) slog.Handler
	grouped bool
}

// NewTraceHandler wraps h.
func NewTraceHandler(h slog.Handler) *TraceHandler {
	return &TraceHandler{root: h, handler: h, replay: func(h slog.Handler) slog.Handler { return h }}
}

func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.handler.Handle(ctx, r)
	}

	attrs := []slog.Attr{
		slog.String(TraceIDKey, sc.TraceID().String()),
		slog.String(SpanIDKey, sc.SpanID().String()),
		slog.String(TraceFlagsKey, sc.TraceFlags().String()),
	}

	// A record's attributes go in the logger's innermost group: once the logger has one, the trace context is added
	// to the root instead, before the logger's attributes and groups.
	if h.grouped {
		return h.replay(h.root.WithAttrs(attrs)).Handle(ctx, r)
	}

	r = r.Clone()
	r.AddAttrs(attrs...)

	return h.handler.Handle(ctx, r)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	return h.with(func(h slog.Handler) slog.Handler { return h.WithAttrs(attrs) }, false)
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return h.with(func(h slog.Handler) slog.Handler { return h.WithGroup(name) }, true)
}

// with is h with the attributes or group that add gives a handler.
func (h *TraceHandler) with(add func(slog.Handler) slog.Handler, group bool) *TraceHandler {
	replay := h.replay

	return &TraceHandler{
		root:    h.root,
		handler: add(h.handler),
		replay:  func(base slog.Handler) slog.Handler { return add(replay(base)) },
		grouped: h.grouped || group,
	}
}
