package api_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const gzipMagic = "\x1f\x8b"

func TestPprof(t *testing.T) {
	e := newEnv(t)

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		code     int
		contains string
	}{
		{"index", http.MethodGet, "/api/v1/pprof/", "", http.StatusOK, "<title>/debug/pprof/</title>"},
		{"heap", http.MethodGet, "/api/v1/pprof/heap", "", http.StatusOK, gzipMagic},
		{"heap after gc", http.MethodGet, "/api/v1/pprof/heap?gc=1", "", http.StatusOK, gzipMagic},
		{"heap text", http.MethodGet, "/api/v1/pprof/heap?debug=1", "", http.StatusOK, "heap profile:"},
		{"allocs", http.MethodGet, "/api/v1/pprof/allocs", "", http.StatusOK, gzipMagic},
		{"goroutine", http.MethodGet, "/api/v1/pprof/goroutine", "", http.StatusOK, gzipMagic},
		{"goroutine dump", http.MethodGet, "/api/v1/pprof/goroutine?debug=2", "", http.StatusOK, "goroutine "},
		{"goroutineleak", http.MethodGet, "/api/v1/pprof/goroutineleak", "", http.StatusOK, gzipMagic},
		{"threadcreate", http.MethodGet, "/api/v1/pprof/threadcreate", "", http.StatusOK, gzipMagic},
		{"delta", http.MethodGet, "/api/v1/pprof/allocs?seconds=1", "", http.StatusOK, gzipMagic},
		{"cmdline", http.MethodGet, "/api/v1/pprof/cmdline", "", http.StatusOK, "api.test"},
		{"cpu", http.MethodGet, "/api/v1/pprof/profile?seconds=1", "", http.StatusOK, gzipMagic},
		{"trace", http.MethodGet, "/api/v1/pprof/trace?seconds=0.1", "", http.StatusOK, "go 1."},
		{"symbol count", http.MethodGet, "/api/v1/pprof/symbol", "", http.StatusOK, "num_symbols: 1"},
		{"symbol", http.MethodPost, "/api/v1/pprof/symbol", "0x1", http.StatusOK, "num_symbols: 1"},
		{"block", http.MethodGet, "/api/v1/pprof/block", "", http.StatusNotFound, ""},
		{"mutex", http.MethodGet, "/api/v1/pprof/mutex", "", http.StatusNotFound, ""},
		{"unknown profile", http.MethodGet, "/api/v1/pprof/nothing", "", http.StatusNotFound, ""},
		{"subpath", http.MethodGet, "/api/v1/pprof/heap/x", "", http.StatusNotFound, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.do(t, tc.method, tc.path, "", tc.body)

			if code != tc.code || !strings.Contains(body, tc.contains) {
				t.Fatalf("%s %s = %d %.200q, want %d with %q", tc.method, tc.path, code, body, tc.code, tc.contains)
			}
		})
	}
}

func TestPprofIndexLinks(t *testing.T) {
	e := newEnv(t)

	_, index := e.do(t, http.MethodGet, "/api/v1/pprof/", "", "")
	links := regexp.MustCompile(`href=['"]([^'"]+)['"]`).FindAllStringSubmatch(index, -1)

	if len(links) == 0 {
		t.Fatal("the index has no links")
	}

	for _, link := range links {
		href := link[1]
		if strings.Contains(href, "://") || strings.HasPrefix(href, "#") {
			continue
		}

		path := "/api/v1/pprof/" + href
		if strings.HasPrefix(href, "profile?") {
			path += "&seconds=1"
		}

		want := http.StatusOK
		if strings.HasPrefix(href, "block?") || strings.HasPrefix(href, "mutex?") {
			want = http.StatusNotFound
		}

		code, body := e.do(t, http.MethodGet, path, "", "")
		if code != want || strings.Contains(body, "<title>/debug/pprof/</title>") {
			t.Errorf("GET %s = %d %.100q", path, code, body)
		}
	}
}
