# deploy/local/Dockerfile — multi-stage build for a single Go hot-path service.
#
# Build context is the repo root (gen/ is versioned, so the build is hermetic
# and needs no network for codegen — ADR-0002 §C).  Pass the service name:
#   docker build --build-arg SERVICE=decision  -f deploy/local/Dockerfile .
#   docker build --build-arg SERVICE=collector -f deploy/local/Dockerfile .

FROM golang:1.26 AS build
WORKDIR /src

# Cache the module graph first.
COPY go.mod go.sum ./
RUN go mod download

# Then the source (gen/ included — versioned generated code).
COPY . .

ARG SERVICE
RUN test -n "$SERVICE" || (echo "SERVICE build-arg is required" && false)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -o /out/app ./services/${SERVICE}/cmd/${SERVICE}/

# Distroless static: no shell, minimal attack surface (matches platform/ intent).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
