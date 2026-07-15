# generalproxy

A small, config-driven reverse proxy in a single Go file. It maps an incoming
`host` + path `prefix` to an `upstream` URL and forwards the request, hot-reloading
the routing table whenever the config file changes — no restart needed.

Built on [`moonrhythm/parapet`](https://github.com/moonrhythm/parapet) (frontend +
graceful shutdown) and its ingress-controller `proxy` package (the reverse proxy).

## Install

```sh
go install github.com/pabloescobranch/generalproxy@latest
```

Or build from source:

```sh
go build -o generalproxy .
```

## Usage

```sh
generalproxy -config config.json -port 8080 [-debug]
```

| Flag       | Default        | Description                                          |
|------------|----------------|------------------------------------------------------|
| `-config`  | `config.json`  | Path to the routes config file.                      |
| `-port`    | `8080`         | Listen port.                                         |
| `-debug`   | `false`        | Log each `host+prefix → upstream` on startup/reload. |

## Configuration

Routes live in a JSON file that is polled for changes and hot-reloaded:

```json
{
  "routes": [
    { "host": "app.example.com", "prefix": "/api", "upstream": "http://backend:8080" },
    { "host": "app.example.com", "prefix": "/",    "upstream": "http://frontend:3000" },
    { "prefix": "/health",                          "upstream": "http://checker:9000" }
  ]
}
```

| Field      | Description                                                                        |
|------------|------------------------------------------------------------------------------------|
| `host`     | Matched case-insensitively; `:port` is stripped. Empty matches any host (wildcard).|
| `prefix`   | Path prefix. Empty defaults to `/` (catch-all). The most specific route wins.      |
| `upstream` | Target URL. A scheme-less value (`backend:8080`) is treated as `host[:port][/path]`.|

Host-specific routes take precedence over the wildcard, and longer prefixes take
precedence over shorter ones. The upstream path prefix is preserved — the backend
receives the original request path.

## How it works

The whole app is one file (`main.go`).

- **Routing table** — a `controller` holds an `http.ServeMux` behind an
  `atomic.Pointer[routeTable]`. A request loads the current pointer and serves
  against an immutable table, while a reload builds a fresh table off to the side
  and flips the pointer in one atomic store — no locks, no half-swapped state.
- **Matching** — delegated to `http.ServeMux`, which provides host+path matching,
  longest-prefix subtree matching, and host-over-wildcard precedence natively.
  Each prefix is registered twice — bare (`/api`) and subtree (`/api/`) — so `/api`
  is served directly instead of redirected; root `/` registers the subtree only.
- **Per-request rewrite** — the handler points `r.Host` / `r.URL.Host` (and scheme)
  at the upstream so platforms that route by the Host header reach the right
  backend. The path (including its prefix) is left untouched. `r.RemoteAddr` is
  cleared so the proxy doesn't append the client IP to `X-Forwarded-For`.
- **Hot reload** — a watcher polls the config file's mtime and rebuilds the table
  on change. `reload` recovers from panics (e.g. a duplicate mux pattern from a bad
  config), so a malformed reload is logged and the previous table keeps serving.
  One shared reverse proxy serves every route; the listen port is fixed at startup.

## Extending it

The file is built as a reusable skeleton. To build a variant proxy, keep the
proxy-agnostic parts unchanged and swap only the routing logic:

- **Keep as-is** — `main`, the `controller` + `atomic.Pointer[routeTable]` hot-swap,
  `ServeHandler` (the `parapet.Middleware` contract, with its `recover`), the
  `watchConfig` mtime-poll loop, and the `reload` panic-recover wrapper.
- **Replace** — the `Route` / `Config` shape (match on a header, method, query
  param, weighted upstreams, …), the normalization in `loadConfig`, and the loop
  body of `reload`: how routes register into the matcher and what the per-request
  handler rewrites before calling the shared proxy (path stripping, header
  injection, load-balancing). If you need something other than host+path matching,
  swap `http.ServeMux` for your own matcher inside `routeTable`.

Invariants to preserve: one shared proxy, atomic table swap, `reload` never
crashes (config errors are returned/logged, never fatal), and a listen port fixed
at startup.
