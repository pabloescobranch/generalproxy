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

- The routing table (`http.ServeMux`) lives behind an `atomic.Pointer`, so reloads
  swap it wholesale without locking the request path.
- One shared reverse proxy serves every route.
- A watcher polls the config file's mtime and rebuilds the table on change; a bad
  config is logged and the previous table keeps serving. The listen port is fixed
  at startup.
