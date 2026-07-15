# generalproxy

A small config-driven reverse proxy. It maps incoming `host` + path `prefix` to
an `upstream` URL and forwards the request, hot-reloading the routing table when
the config file changes. Built on `moonrhythm/parapet` (frontend + graceful
shutdown) and its ingress-controller `proxy` package (the actual reverse proxy).

This file also serves as a **reference for building another custom proxy** from
the same skeleton — see [Reusable skeleton](#reusable-skeleton) and
[Building a variant proxy](#building-a-variant-proxy).

## Layout

The whole app is one file:

- `main.go` — flags, config loading, routing, hot reload.
- `config.json` — routes (gitignored; provide your own).

## How it works

- **`controller`** holds the live routing table (`http.ServeMux`) behind an
  `atomic.Pointer[routeTable]`, so `reload` can swap it wholesale without locking
  the request path. One shared `proxy.Proxy` serves every route.
- **Routing** is delegated to `http.ServeMux`, which gives host+path matching,
  longest-prefix subtree matching, and host-over-wildcard precedence for free.
  Each prefix is registered twice — bare (`/api`) and subtree (`/api/`) — so
  `/api` is served directly instead of redirected; root `/` registers the
  subtree only.
- **Per-request rewrite**: the handler sets `r.Host` / `r.URL.Host` (and scheme)
  to the upstream so platforms that route by the Host header hit the right
  backend. The path (including its prefix) is left untouched, so the backend sees
  the original path. `r.RemoteAddr` is cleared so the proxy doesn't append the
  client IP to `X-Forwarded-For`.
- **Hot reload**: `watchConfig` polls the config mtime on an interval and calls
  `reload`. `reload` recovers from panics (e.g. a duplicate mux pattern from a
  bad config) so a malformed reload logs and keeps the current table serving.
  Only routes reload — the listen port is fixed at startup.

## Config

```json
{ "routes": [ { "host": "app.example.com", "prefix": "/api", "upstream": "http://backend:8080" } ] }
```

- `host` — matched case-insensitively, `:port` stripped. Empty = any host.
- `prefix` — path prefix. Empty defaults to `/` (catch-all).
- `upstream` — target URL. A scheme-less value (`backend:8080`) is treated as
  host[:port][/path], not a path.

## Run

```sh
go build ./...
go run . -config config.json -port 8080 [-debug]
```

`-debug` logs each `host+prefix → upstream` registration on startup and reload.

## Reusable skeleton

The parts below are proxy-agnostic — copy them as-is into a new proxy and only
change the routing/rewrite logic.

- **`main`** — parse flags, `loadConfig`, `newController`, wire a
  `parapet.NewFrontend()` with `s.Addr`, `s.Use(ctrl)`, `s.RegisterOnShutdown(cancel)`,
  then `go ctrl.watchConfig(...)` and `s.ListenAndServe()`.
- **`controller` + `atomic.Pointer[routeTable]`** — the hot-swap mechanism. A
  request does `ctrl.routes.Load()` and serves against an immutable table; `reload`
  builds a fresh table off to the side and flips the pointer in one store. No
  locks, no half-swapped state. `routeTable` is a struct (not a bare `http.Handler`)
  so you can add fields later without touching the swap.
- **`ServeHandler(next http.Handler) http.Handler`** — the `parapet.Middleware`
  contract. Wrap the served table in a `defer recover()` so one bad request can't
  kill the process (client gets a 500).
- **`watchConfig`** — mtime-poll loop with `ctx` cancellation and a `time.Ticker`.
  Every reload error is logged and swallowed; the current table keeps serving.
- **`reload` with `defer recover()`** — never panics out; any config-shape error
  becomes a returned/logged error.

The **proxy-specific** parts you'd replace: the `Route`/`Config` shape, the mux
build inside `reload` (registration + per-request rewrite), and the normalization
in `loadConfig`.

## Building a variant proxy

1. Copy `main.go`; keep `main`, `controller`, `routeTable`, `ServeHandler`,
   `watchConfig`, and the `reload` panic-recover wrapper unchanged.
2. Redefine `Route`/`Config` for the new matching dimension (e.g. header, method,
   query param, weighted upstreams) and adjust `loadConfig` normalization.
3. Rewrite the loop body of `reload`: how routes register into the mux (or a
   different matcher) and what the per-request handler rewrites before calling
   `ctrl.proxy.ServeHTTP`. Common rewrites to add here: path stripping, header
   injection, upstream load-balancing.
4. If you need something other than host+path matching, swap `http.ServeMux` for
   your own matcher inside `routeTable` — the atomic swap and everything else stays.

Keep the same invariants: one shared `proxy.Proxy`, atomic table swap, reload
never crashes, listen port fixed at startup.

## Conventions

- Keep it a single file and dependency-light; don't add packages or abstractions
  unless a second real use appears.
- `reload` must never panic out — any config-shape error becomes a returned/logged
  error, never a crash.
- Comments explain the non-obvious *why* (host rewrite, XFF, bare+subtree
  registration, scheme-less upstream); keep them concise.
