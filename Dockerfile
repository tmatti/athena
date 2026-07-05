# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
# NOTE: behind a corporate/MITM proxy, `go mod download` needs a trusted CA
# bundle and (if TLS still fails) may require building with `--network host`
# so the container can see the proxy's certs, e.g.:
#   docker build --network host -t athena .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /athena ./cmd/athena

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /athena /athena

EXPOSE 8080

ENTRYPOINT ["/athena"]
