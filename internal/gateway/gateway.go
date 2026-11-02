package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"github.com/prometheus/client_golang/prometheus"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const closeTimeout = 5 * time.Second

type Settings struct {
	Host     string
	Port     int
	TLS      bool
	SystemID string
	Password string
}

func SettingsOf(s db.SMSC) Settings {
	return Settings{Host: s.Host, Port: s.Port, TLS: s.TLS, SystemID: s.SystemID, Password: s.Password}
}

type Status string

const (
	StatusUnset        Status = "unset"
	StatusConnecting   Status = "connecting"
	StatusBound        Status = "bound"
	StatusDisconnected Status = "disconnected"
)

type State struct {
	Status      Status
	Since       time.Time
	SystemID    string
	LastError   string
	LastErrorAt time.Time
}

type Config struct {
	Handler        smpp.Handler
	TracerProvider trace.TracerProvider
	Logger         *slog.Logger
}

const tracerName = "github.com/ellanetworks/simota/internal/gateway"

type Gateway struct {
	cfg    Config
	tracer trace.Tracer
	submit *prometheus.HistogramVec

	mu       sync.Mutex
	settings *Settings
	client   *smpp.Client
	since    time.Time
}

func New(cfg Config) *Gateway {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	tp := cfg.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return &Gateway{
		cfg: cfg, tracer: tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL)), submit: newSubmitDuration(),
		since: time.Now(),
	}
}

func (g *Gateway) Apply(ctx context.Context, s *Settings) error {
	g.mu.Lock()

	if s != nil && g.settings != nil && *s == *g.settings {
		g.mu.Unlock()

		return nil
	}

	old := g.client
	g.client, g.settings, g.since = nil, nil, time.Now()

	var err error

	if s != nil {
		var c *smpp.Client

		c, err = g.newClient(*s)
		if err == nil {
			g.client, g.settings = c, new(*s)
			c.Start()
		}
	}
	g.mu.Unlock()

	if old != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()

		_ = old.Close(ctx)
	}

	return err
}

func (g *Gateway) newClient(s Settings) (*smpp.Client, error) {
	var tlsConfig *tls.Config
	if s.TLS {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return smpp.NewClient(smpp.ClientConfig{
		Addr: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)),
		TLS:  tlsConfig,
		Bind: smpp.Bind{
			ID: smpp.CmdBindTransceiver, SystemID: s.SystemID, Password: s.Password,
			InterfaceVersion: smpp.InterfaceVersion,
		},
		Handler: g.cfg.Handler,
		Logger:  g.cfg.Logger,
	})
}

func (g *Gateway) SubmitSM(ctx context.Context, m *smpp.SubmitSM) (id string, err error) {
	g.mu.Lock()
	c, s := g.client, g.settings
	g.mu.Unlock()

	if trace.SpanContextFromContext(ctx).IsValid() {
		attrs := tracing.SMPPAttributes(smpp.CmdSubmitSM)
		if s != nil {
			attrs = append(attrs, semconv.ServerAddress(s.Host), semconv.ServerPort(s.Port))
		}

		var span trace.Span

		ctx, span = g.tracer.Start(ctx, smpp.CmdSubmitSM.String(), trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attrs...))

		defer func() {
			var status *smpp.CommandStatus
			if err == nil {
				status = new(smpp.StatusOK)
			}

			tracing.EndSMPP(span, status, err)
		}()
	}

	if c == nil {
		return "", smpp.ErrNoSession
	}

	start := time.Now()

	defer func() { g.observeSubmit(start, err) }()

	return c.SubmitSM(ctx, m)
}

func (g *Gateway) Bound() bool {
	g.mu.Lock()
	c := g.client
	g.mu.Unlock()

	return c != nil && c.State().Status == smpp.ConnBound
}

func (g *Gateway) State() State {
	g.mu.Lock()
	c, since := g.client, g.since
	g.mu.Unlock()

	if c == nil {
		return State{Status: StatusUnset, Since: since}
	}

	st := c.State()
	out := State{Status: StatusDisconnected, Since: st.Since, SystemID: st.SystemID, LastErrorAt: st.LastErrorAt}

	switch st.Status {
	case smpp.ConnBound:
		out.Status = StatusBound
	case smpp.ConnConnecting:
		out.Status = StatusConnecting
	case smpp.ConnDisconnected, smpp.ConnClosed:
	}

	if st.LastError != nil {
		out.LastError = errorText(st.LastError)
	}

	return out
}

func errorText(err error) string {
	s := err.Error()

	var se *smpp.StatusError
	if errors.As(err, &se) {
		s = strings.Replace(s, se.Error(), se.Status.String(), 1)
	}

	s = strings.ReplaceAll(s, "smpp: ", "")
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

func (g *Gateway) Close(ctx context.Context) error {
	g.mu.Lock()
	c := g.client
	g.client, g.settings = nil, nil
	g.mu.Unlock()

	if c == nil {
		return nil
	}

	return c.Close(ctx)
}
