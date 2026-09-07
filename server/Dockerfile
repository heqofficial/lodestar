# Build stage
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download
COPY server/ ./server/
# Static, CGO-free binary for any linux arch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lodestard ./server/cmd/lodestard

# Runtime stage
FROM alpine:3.21
RUN adduser -D -H -u 10001 lodestar
USER lodestar
WORKDIR /data
COPY --from=build /out/lodestard /usr/local/bin/lodestard
EXPOSE 8443
VOLUME ["/data"]
# Real probe: only healthy when the store answers (busybox wget ships with alpine).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O - http://127.0.0.1:8443/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/lodestard"]
CMD ["-addr", ":8443", "-data", "/data/lodestar.db"]