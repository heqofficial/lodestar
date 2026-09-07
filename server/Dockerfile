# Build stage
FROM golang:1.24-alpine AS build
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
ENTRYPOINT ["/usr/local/bin/lodestard"]
CMD ["-addr", ":8443", "-data", "/data/lodestar.db"]