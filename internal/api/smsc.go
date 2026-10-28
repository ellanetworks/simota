package api

import (
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"time"

	"github.com/ellanetworks/simota/internal/db"
	"github.com/ellanetworks/simota/internal/gateway"
)

const (
	// defaultValiditySeconds is the validity of an SMSC not set yet: a day bounds how long an unreachable card holds
	// its job.
	defaultValiditySeconds = 24 * 60 * 60
	// defaultPort is the port IANA registers for SMPP, which SMPP 3.4 does not name.
	defaultPort = 2775
	// maxSystemID and maxPassword are the longest system_id and password of a bind, without their NULL terminator
	// (SMPP 3.4 section 4.1.1, table 4-1).
	maxSystemID = 15
	maxPassword = 8
)

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// SMSCSettings is the SMSC without its password, which the API never returns, and the state of simota's bind to it.
type SMSCSettings struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	TLS             bool   `json:"tls"`
	SystemID        string `json:"system_id"`
	Originator      string `json:"originator"`
	ValiditySeconds int64  `json:"validity_seconds"`
	// PasswordSet is whether a password is set: the SMSC is usable.
	PasswordSet bool      `json:"password_set"`
	Bind        BindState `json:"bind"`
}

// BindState is simota's SMPP session to the SMSC.
type BindState struct {
	Bound bool `json:"bound"`
	// Status is unset, connecting, bound or disconnected, since Since.
	Status   string    `json:"status"`
	Since    time.Time `json:"since"`
	SystemID string    `json:"smsc_system_id,omitempty"`
	// LastError is the last failure to connect, to bind, or of the session, at LastErrorAt.
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

type SMSCParams struct {
	Host string `json:"host"`
	// Port is 2775 when absent.
	Port     int    `json:"port"`
	TLS      bool   `json:"tls"`
	SystemID string `json:"system_id"`
	// Password replaces the password set; empty keeps it.
	Password        string `json:"password"`
	Originator      string `json:"originator"`
	ValiditySeconds int64  `json:"validity_seconds"`
}

func GetSMSC(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := cfg.Store.GetSMSC(r.Context())
		if errors.Is(err, db.ErrNotFound) {
			writeResponse(w, SMSCSettings{Port: defaultPort, ValiditySeconds: defaultValiditySeconds, Bind: bindState(cfg)},
				http.StatusOK, cfg.Logger)

			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get the SMSC", err, cfg.Logger)
			return
		}

		writeResponse(w, smscResponse(cfg, s), http.StatusOK, cfg.Logger)
	})
}

// printable reports whether s is printable ASCII, as SMPP's C-Octet Strings of system_id and password carry it.
func printable(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}

	return true
}

func validHost(h string) bool {
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}

	return len(h) <= 253 && hostnameRE.MatchString(h)
}

func UpdateSMSC(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p SMSCParams
		if !decodeBody(w, r, &p, cfg) {
			return
		}

		if p.Port == 0 {
			p.Port = defaultPort
		}

		bad := func(message string) { writeError(w, http.StatusBadRequest, message, nil, cfg.Logger) }

		switch {
		case !validHost(p.Host):
			bad("host must be a host name or an IP address, such as 127.0.0.1")
			return
		case p.Port < 1 || p.Port > 65535:
			bad("port must be between 1 and 65535")
			return
		case p.SystemID == "" || len(p.SystemID) > maxSystemID || !printable(p.SystemID):
			bad("system_id must be 1 to 15 printable ASCII characters")
			return
		case len(p.Password) > maxPassword || !printable(p.Password):
			bad("password must be 1 to 8 printable ASCII characters")
			return
		case !msisdnRE.MatchString(p.Originator):
			bad("originator must be E.164, such as +15550001000")
			return
		case p.ValiditySeconds < 1 || p.ValiditySeconds > maxValiditySeconds:
			bad("validity_seconds must be between 1 and 38102400")
			return
		}

		_, err := cfg.Store.GetSMSC(r.Context())
		if errors.Is(err, db.ErrNotFound) && p.Password == "" {
			bad("password is required")
			return
		}

		s := db.SMSC{
			Host: p.Host, Port: p.Port, TLS: p.TLS, SystemID: p.SystemID, Password: p.Password, Originator: p.Originator,
			Validity: time.Duration(p.ValiditySeconds) * time.Second,
		}
		if err := cfg.Store.PutSMSC(r.Context(), s); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to update the SMSC", err, cfg.Logger)
			return
		}

		updated, err := cfg.Store.GetSMSC(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get the SMSC", err, cfg.Logger)
			return
		}

		// The session binds again only when its connection settings changed.
		if cfg.Gateway != nil {
			if err := cfg.Gateway.Apply(new(gateway.SettingsOf(updated))); err != nil {
				cfg.Logger.Error("failed to connect to the SMSC", "error", err)
			}
		}

		if cfg.Notify != nil {
			cfg.Notify()
		}

		writeResponse(w, smscResponse(cfg, updated), http.StatusOK, cfg.Logger)
	})
}

// maxValiditySeconds is the longest validity period an SMS-SUBMIT can tell, 63 weeks (TS 23.040 clause 9.2.3.12.1).
const maxValiditySeconds = 63 * 7 * 24 * 60 * 60

func bindState(cfg Config) BindState {
	if cfg.Gateway == nil {
		return BindState{Status: string(gateway.StatusUnset)}
	}

	st := cfg.Gateway.State()
	b := BindState{Bound: st.Bound(), Status: string(st.Status), Since: st.Since.UTC(), SystemID: st.SystemID, LastError: st.LastError}

	if !st.LastErrorAt.IsZero() {
		b.LastErrorAt = new(st.LastErrorAt.UTC())
	}

	return b
}

func smscResponse(cfg Config, s db.SMSC) SMSCSettings {
	return SMSCSettings{
		Host: s.Host, Port: s.Port, TLS: s.TLS, SystemID: s.SystemID, Originator: s.Originator,
		ValiditySeconds: int64(s.Validity / time.Second), PasswordSet: s.Password != "", Bind: bindState(cfg),
	}
}
