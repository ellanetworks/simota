package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/simota/internal/logging"
	"go.opentelemetry.io/otel/trace"
)

const (
	traceID = "0af7651916cd43dd8448eb211c80319c"
	spanID  = "b7ad6b7169203331"
)

func traced(t *testing.T) context.Context {
	t.Helper()

	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		t.Fatal(err)
	}

	sid, err := trace.SpanIDFromHex(spanID)
	if err != nil {
		t.Fatal(err)
	}

	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled,
	}))
}

func record(t *testing.T, log func(l *slog.Logger)) map[string]any {
	t.Helper()

	var buf bytes.Buffer

	log(logging.New(&buf, slog.LevelDebug))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %q, want one record", buf.String())
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("%s: %v", lines[0], err)
	}

	return m
}

func hasTrace(m map[string]any) bool {
	return m[logging.TraceIDKey] == traceID && m[logging.SpanIDKey] == spanID && m[logging.TraceFlagsKey] == "01"
}

func TestFormat(t *testing.T) {
	m := record(t, func(l *slog.Logger) { l.Warn("slow", slog.Duration("took", 1500*time.Millisecond)) })

	if m["level"] != "warn" || m["msg"] != "slow" || m["took"] != "1.5s" || m["ts"] == nil || m["time"] != nil {
		t.Fatalf("record = %v", m)
	}
}

func TestTraceContext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		log   func(ctx context.Context, l *slog.Logger)
		check func(m map[string]any) bool
	}{
		{
			name: "plain",
			log:  func(ctx context.Context, l *slog.Logger) { l.InfoContext(ctx, "m", slog.Int("job", 1)) },
			check: func(m map[string]any) bool {
				return m["job"] == 1.0
			},
		},
		{
			name: "attributes",
			log: func(ctx context.Context, l *slog.Logger) {
				l.With(slog.String("smsc", "a")).InfoContext(ctx, "m", slog.Int("job", 1))
			},
			check: func(m map[string]any) bool {
				return m["smsc"] == "a" && m["job"] == 1.0
			},
		},
		{
			name: "group",
			log: func(ctx context.Context, l *slog.Logger) {
				l.With(slog.String("smsc", "a")).WithGroup("g").With(slog.String("in", "b")).InfoContext(ctx, "m",
					slog.Int("job", 1))
			},
			check: func(m map[string]any) bool {
				g, ok := m["g"].(map[string]any)
				return ok && m["smsc"] == "a" && g["in"] == "b" && g["job"] == 1.0 && g[logging.TraceIDKey] == nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := record(t, func(l *slog.Logger) { tc.log(traced(t), l) })
			if !hasTrace(m) || !tc.check(m) {
				t.Errorf("traced record = %v", m)
			}

			m = record(t, func(l *slog.Logger) { tc.log(context.Background(), l) })
			if m[logging.TraceIDKey] != nil || m[logging.SpanIDKey] != nil || m[logging.TraceFlagsKey] != nil || !tc.check(m) {
				t.Errorf("untraced record = %v", m)
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	var buf bytes.Buffer

	l := logging.New(&buf, slog.LevelInfo)
	l.DebugContext(traced(t), "hidden")

	if buf.Len() != 0 {
		t.Fatalf("logged %q below the level", buf.String())
	}
}
