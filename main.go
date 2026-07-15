package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moonrhythm/parapet"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	port := flag.String("port", "8080", "listen port")
	debug := flag.Bool("debug", false, "log each host+prefix registered on startup and reload")
	flag.Parse()

	if *debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	c, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	ctrl, err := newController(c.Routes)
	if err != nil {
		slog.Error("build routes", "err", err)
		os.Exit(1)
	}

	// Cancelled on graceful shutdown so the config watcher stops cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := ":" + *port
	s := parapet.NewFrontend()
	s.Addr = addr
	s.RegisterOnShutdown(cancel)
	s.Use(ctrl)

	go ctrl.watchConfig(ctx, *configPath, 1*time.Minute)

	slog.Info("listening", "addr", addr, "routes", len(c.Routes))
	if err := s.ListenAndServe(); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

// --- config ---

// Config is loaded from a JSON file. Each route maps an incoming host+prefix to
// an upstream URL; the most specific route wins.
type Config struct {
	Routes []Route `json:"routes"`
}

type Route struct {
	Host     string `json:"host"`
	Prefix   string `json:"prefix"`
	Upstream string `json:"upstream"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, err
	}
	// Normalize each route: lowercase + strip :port on the host (the mux matches
	// hosts case-insensitively without the port), and default empty prefix to "/".
	for i := range c.Routes {
		r := &c.Routes[i]
		host := strings.ToLower(r.Host)
		if j := strings.IndexByte(host, ':'); j >= 0 {
			host = host[:j]
		}
		r.Host = host
		if r.Prefix == "" {
			r.Prefix = "/"
		}
	}
	return &c, nil
}

// --- controller ---

// controller holds the live routing table behind an atomic pointer so it can be
// swapped wholesale on reload without locking the request path. A single reverse
// proxy is shared by every route.
//
// The table is stored via atomic.Pointer so requests and the reload goroutine
// never race: a request loads the current pointer and serves against a table
// that stays immutable, while reload builds a brand-new table off to the side
// and flips the pointer in one atomic store. No locks, no half-swapped state —
// each request sees either the old table or the new one, never a mix.
//
// It's wrapped in routeTable (rather than storing the mux directly) because
// atomic.Pointer swaps exactly one pointer: bundling the routing state into a
// struct lets the whole table be replaced in a single store, and leaves room to
// add fields to it later without changing the swap mechanism.
type controller struct {
	proxy  *httputil.ReverseProxy
	routes atomic.Pointer[routeTable]
}

type routeTable struct {
	mux http.Handler
}

func newController(routes []Route) (*controller, error) {
	// Clone keeps the stdlib dial/handshake timeouts + keepalive; we only tune
	// the ingress→ingress hop: warm per-host pool, pass encoded bodies through,
	// patient header timeout for slow upstream backends. TLS verification stays on.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 100
	tr.IdleConnTimeout = 90 * time.Second
	tr.DisableCompression = true
	tr.ResponseHeaderTimeout = 3 * time.Minute

	ctrl := &controller{
		// The per-route handler sets r.URL fully before delegating, so the
		// Director has nothing to do — but it must be non-nil or ServeHTTP panics.
		proxy: &httputil.ReverseProxy{
			Director:   func(*http.Request) {},
			BufferPool: newBufferPool(),
			Transport:  tr,
		},
	}
	// init empty mux
	ctrl.routes.Store(&routeTable{mux: http.NewServeMux()})

	if err := ctrl.reload(routes); err != nil {
		return nil, err
	}
	return ctrl, nil
}

// ServeHandler implements parapet.Middleware. It delegates to the currently
// loaded mux; the next handler is unused (the mux is terminal). A panic while
// serving is recovered so one bad request can't take down the process; the
// client gets a 500 (unless bytes were already written).
func (ctrl *controller) ServeHandler(_ http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(r.Context(), "serve panic", "err", rec, "host", r.Host, "path", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		ctrl.routes.Load().mux.ServeHTTP(w, r)
	})
}

// reload builds a fresh mux and swaps it in atomically. It recovers from any
// panic during the build (e.g. a duplicate mux pattern) so a malformed config
// can't crash the caller — the current table keeps serving.
func (ctrl *controller) reload(routes []Route) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			slog.Error("reload panic", "err", err)
		}
	}()

	slog.Debug("reload start", "routes", len(routes))

	// http.ServeMux handles host+path matching, longest-prefix subtree matching,
	// and host-over-wildcard precedence natively. Each prefix is registered as
	// both the bare path ("/3") and the subtree ("/3/") so "/3" is served
	// directly instead of redirected; the root "/" registers the subtree only.
	mux := http.NewServeMux()
	for _, r := range routes {
		raw := r.Upstream
		if !strings.Contains(raw, "://") {
			raw = "//" + raw // make url.Parse read it as host[:port][/path], not a path
		}
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		scheme, host, suffix := u.Scheme, u.Host, u.Path

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = ""
			r.Host = host         // route by upstream host, not the incoming public host
			r.URL.Scheme = scheme // route by upstream scheme; "" is fine, the proxy treats it as http
			r.URL.Host = host
			// preserve the original suffix of the upstream URL, if any, so a route like "/3" -> "http://upstream/3" works
			if suffix != "" && !strings.HasPrefix(r.URL.Path, suffix) {
				r.URL.Path = suffix + r.URL.Path
			}
			ctrl.proxy.ServeHTTP(w, r)
		})

		// One route answers under several prefix variants, all proxying to the
		// same upstream: the prefix as written, its lowercase form, and the last
		// path segment of the upstream URL (e.g. ".../app" -> "/app").
		variants := []string{r.Prefix, strings.ToLower(r.Prefix)}
		if seg := path.Base(suffix); seg != "" && seg != "/" && seg != "." {
			variants = append(variants, "/"+seg)
		}

		// Register each variant once as both bare path and subtree; dedupe so a
		// prefix that already equals a variant isn't registered twice.
		seen := make(map[string]bool)
		for _, prefix := range variants {
			if seen[prefix] {
				continue
			}
			seen[prefix] = true

			src := r.Host + strings.TrimSuffix(prefix, "/")
			if prefix != "/" {
				mux.Handle(src, handler) // exact bare path
			}
			mux.Handle(src+"/", handler) // subtree
			slog.Debug("register route", "host", r.Host, "prefix", prefix, "upstream", r.Upstream, "src", src)
		}
	}
	ctrl.routes.Store(&routeTable{mux: mux})
	slog.Debug("reload done", "routes", len(routes))
	return nil
}

// watchConfig polls the config file every interval and reloads the routing
// table when the file changes. A bad reload is logged and the current table
// keeps serving (Listen is fixed at startup, so only routes are reloaded).
func (ctrl *controller) watchConfig(ctx context.Context, path string, interval time.Duration) {
	lastMod := time.Time{}
	if fi, err := os.Stat(path); err == nil {
		lastMod = fi.ModTime()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		fi, err := os.Stat(path)
		if err != nil {
			slog.Error("watch config: stat", "err", err)
			continue
		}
		if !fi.ModTime().After(lastMod) {
			continue // unchanged since last check
		}

		c, err := loadConfig(path)
		if err != nil {
			slog.Error("watch config: load", "err", err)
			continue
		}
		if err := ctrl.reload(c.Routes); err != nil {
			slog.Error("watch config: reload", "err", err)
			continue
		}
		lastMod = fi.ModTime()
		slog.Info("config reloaded", "routes", len(c.Routes))
	}
}

const bufferSize = 16 * 1024

type bufferPool struct {
	sync.Pool
}

func newBufferPool() *bufferPool {
	return &bufferPool{
		Pool: sync.Pool{
			New: func() any { return make([]byte, bufferSize) },
		},
	}
}

func (p *bufferPool) Get() []byte {
	return p.Pool.Get().([]byte)
}

func (p *bufferPool) Put(b []byte) {
	p.Pool.Put(b)
}
