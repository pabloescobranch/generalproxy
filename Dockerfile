# syntax=docker/dockerfile:1

# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache modules separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary, no cgo, stripped.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /generalproxy .

# --- run ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /generalproxy /usr/local/bin/generalproxy

WORKDIR /app
EXPOSE 8080

# config.json is not baked in (it's treated as local/secret). Mount it at runtime:
#   docker run -p 8080:8080 -v $PWD/config.json:/app/config.json generalproxy
ENTRYPOINT ["generalproxy"]
CMD ["-config", "/app/config.json", "-port", "8080"]
