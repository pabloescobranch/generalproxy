package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// echoUpstream returns a server that writes back the request path it received,
// so tests can assert how the proxy rewrote the path before forwarding.
func echoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

// TestReloadRouteVariants: a route whose upstream carries a path segment expands
// into three prefixes — the prefix as written, its lowercase form, and the
// upstream's last segment — all proxying to the same upstream.
func TestReloadRouteVariants(t *testing.T) {
	t.Parallel()

	upstream := echoUpstream(t)
	ctrl, err := newController([]Route{
		{Host: "localhost", Prefix: "/APP", Upstream: upstream.URL + "/seg"},
	})
	if err != nil {
		t.Fatalf("newController: %v", err)
	}
	h := ctrl.ServeHandler(nil)

	cases := []struct {
		name     string
		path     string
		wantCode int
		wantPath string // path the upstream received ("" = skip check)
	}{
		{"prefix as written", "/APP", http.StatusOK, "/seg/APP"},
		{"lowercase prefix", "/app", http.StatusOK, "/seg/app"},
		{"upstream last segment", "/seg", http.StatusOK, "/seg"},
		{"subtree under prefix", "/APP/foo", http.StatusOK, "/seg/APP/foo"},
		{"unregistered path", "/nope", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "localhost"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantPath != "" && rec.Body.String() != tc.wantPath {
				t.Errorf("upstream saw path %q, want %q", rec.Body.String(), tc.wantPath)
			}
		})
	}
}

// TestReloadBareHostUpstream: an upstream with no path segment expands only into
// the prefix and its lowercase form — no third variant, and no root catch-all.
func TestReloadBareHostUpstream(t *testing.T) {
	t.Parallel()

	upstream := echoUpstream(t)
	ctrl, err := newController([]Route{
		{Host: "localhost", Prefix: "/APP", Upstream: upstream.URL},
	})
	if err != nil {
		t.Fatalf("newController: %v", err)
	}
	h := ctrl.ServeHandler(nil)

	cases := []struct {
		name     string
		path     string
		wantCode int
	}{
		{"prefix as written", "/APP", http.StatusOK},
		{"lowercase prefix", "/app", http.StatusOK},
		{"no root catch-all", "/", http.StatusNotFound},
		{"no derived segment", "/seg", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "localhost"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestReloadDedupesIdenticalVariants: when the prefix already equals the
// upstream's last segment, all three variants collapse to one. Without the
// dedupe, mux.Handle would panic on the duplicate pattern and reload would
// surface that as an error.
func TestReloadDedupesIdenticalVariants(t *testing.T) {
	t.Parallel()

	if _, err := newController([]Route{
		{Host: "localhost", Prefix: "/seg", Upstream: "https://upstream.test/seg"},
	}); err != nil {
		t.Fatalf("newController with self-matching variant: %v", err)
	}
}
