# generalproxy

A small config-driven reverse proxy. It maps incoming `host` + path `prefix` to
an `upstream` URL and forwards the request, hot-reloading the routing table when
the config file changes. Built on `moonrhythm/parapet` (frontend + graceful
shutdown) and its ingress-controller `proxy` package (the actual reverse proxy).

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

## Conventions

- Keep it a single file and dependency-light; don't add packages or abstractions
  unless a second real use appears.
- `reload` must never panic out — any config-shape error becomes a returned/logged
  error, never a crash.
- Comments explain the non-obvious *why* (host rewrite, XFF, bare+subtree
  registration, scheme-less upstream); keep them concise.
