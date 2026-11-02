package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const (
	TraceIDKey    = "trace_id"
	SpanIDKey     = "span_id"
	TraceFlagsKey = "trace_flags"
)

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

type TraceHandler struct {
	root    slog.Handler
	handler slog.Handler
	replay  func(slog.Handler) slog.Handler
	grouped bool
}

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

func (h *TraceHandler) with(add func(slog.Handler) slog.Handler, group bool) *TraceHandler {
	replay := h.replay

	return &TraceHandler{
		root:    h.root,
		handler: add(h.handler),
		replay:  func(base slog.Handler) slog.Handler { return add(replay(base)) },
		grouped: h.grouped || group,
	}
}
