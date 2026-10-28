// Package gateway keeps simota's SMPP 3.4 session to Ella SMSC: one transceiver bind per process (SMPP 3.4 section
// 2.2), bound again with a backoff whenever it is lost, with the SMSC settings as set.
package gateway

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/smsc/smpp"
)

// closeTimeout bounds the unbind of a session being replaced.
const closeTimeout = 5 * time.Second

// Settings are what the session connects and binds with.
type Settings struct {
	Host     string
	Port     int
	TLS      bool
	SystemID string
	Password string
}

// SettingsOf are the connection settings of an SMSC.
func SettingsOf(s db.SMSC) Settings {
	return Settings{Host: s.Host, Port: s.Port, TLS: s.TLS, SystemID: s.SystemID, Password: s.Password}
}

// Status is where the session is.
type Status string

const (
	// StatusUnset is no SMSC set.
	StatusUnset        Status = "unset"
	StatusConnecting   Status = "connecting"
	StatusBound        Status = "bound"
	StatusDisconnected Status = "disconnected"
)

// State is the session's state, as the API shows it.
type State struct {
	Status Status
	// Since is when Status was entered.
	Since time.Time
	// SystemID is the SMSC's, while bound.
	SystemID string
	// LastError is the last failure to connect, to bind, or of the bound session, at LastErrorAt.
	LastError   string
	LastErrorAt time.Time
}

// Bound reports whether the session is bound.
func (s State) Bound() bool { return s.Status == StatusBound }

// Config configures a Gateway.
type Config struct {
	// Handler handles the SMSC's deliver_sm: delivery receipts and SIM answers.
	Handler smpp.Handler
	// Session sets the SMPP timers and windows; zero values take smpp's defaults.
	Session smpp.SessionConfig
	// MinBackoff and MaxBackoff bound the wait before binding again; zero values take smpp's defaults.
	MinBackoff, MaxBackoff time.Duration
	// TLS is the base TLS configuration of a TLS connection; nil is the system's roots, TLS 1.2 at least.
	TLS    *tls.Config
	Logger *slog.Logger
}

// Gateway is the session to the SMSC. It is safe for concurrent use.
type Gateway struct {
	cfg Config

	mu       sync.Mutex
	settings *Settings
	client   *smpp.Client
	since    time.Time
}

func New(cfg Config) *Gateway {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	return &Gateway{cfg: cfg, since: time.Now()}
}

// Apply connects with s, after unbinding a session with other settings, or disconnects when s is nil. Settings
// equal to those in use change nothing.
func (g *Gateway) Apply(s *Settings) error {
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
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()

		_ = old.Close(ctx)
	}

	return err
}

func (g *Gateway) newClient(s Settings) (*smpp.Client, error) {
	var tlsConfig *tls.Config

	if s.TLS {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		if g.cfg.TLS != nil {
			tlsConfig = g.cfg.TLS.Clone()
		}
	}

	return smpp.NewClient(smpp.ClientConfig{
		Addr: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)),
		TLS:  tlsConfig,
		Bind: smpp.Bind{
			ID: smpp.CmdBindTransceiver, SystemID: s.SystemID, Password: s.Password,
			InterfaceVersion: smpp.InterfaceVersion,
		},
		Session:    g.cfg.Session,
		Handler:    g.cfg.Handler,
		MinBackoff: g.cfg.MinBackoff,
		MaxBackoff: g.cfg.MaxBackoff,
		Logger:     g.cfg.Logger,
	})
}

// SubmitSM submits a short message and returns its message_id, or smpp.ErrNoSession while not bound.
func (g *Gateway) SubmitSM(ctx context.Context, m *smpp.SubmitSM) (string, error) {
	g.mu.Lock()
	c := g.client
	g.mu.Unlock()

	if c == nil {
		return "", smpp.ErrNoSession
	}

	return c.SubmitSM(ctx, m)
}

// Bound reports whether the session is bound.
func (g *Gateway) Bound() bool {
	g.mu.Lock()
	c := g.client
	g.mu.Unlock()

	return c != nil && c.State().Status == smpp.ConnBound
}

// WaitBound waits until the session is bound, or ctx ends.
func (g *Gateway) WaitBound(ctx context.Context) error {
	g.mu.Lock()
	c := g.client
	g.mu.Unlock()

	if c == nil {
		return smpp.ErrNoSession
	}

	return c.WaitBound(ctx)
}

// State is the session's state.
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
		out.LastError = st.LastError.Error()
	}

	return out
}

// Close unbinds the session until ctx ends.
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
