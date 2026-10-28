package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/simota/internal/config"
)

func write(t *testing.T, s string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "simota.yaml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestLoad(t *testing.T) {
	cfg, err := config.Load(write(t, "logging:\n  level: debug\ndb:\n  path: simota.db\napi:\n  address: 127.0.0.1\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Logging.Level != slog.LevelDebug || cfg.DB.Path != "simota.db" || cfg.API.Address.String() != "127.0.0.1" ||
		cfg.API.Port != 5020 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadRejects(t *testing.T) {
	for _, tc := range []struct{ yaml, err string }{
		{"api:\n  address: 127.0.0.1\n", "db.path"},
		{"db:\n  path: simota.db\n", "api.address"},
		{"db:\n  path: simota.db\napi:\n  address: 127.0.0.1\n  port: 70000\n", "api.port"},
		{"db:\n  path: simota.db\napi:\n  address: 127.0.0.1\nsmsc: {}\n", "smsc"},
		{"db:\n  path: simota.db\napi:\n  address: localhost\n", "parse config"},
	} {
		if _, err := config.Load(write(t, tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Fatalf("Load(%q) = %v, want %q", tc.yaml, err, tc.err)
		}
	}

	if _, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
