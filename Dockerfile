# Build the binary against the full toolchain, then ship only the binary. The
# runtime image carries no compiler, shell or package manager, which keeps the
# image small and the attack surface minimal.
FROM golang:1.24-alpine AS build

WORKDIR /src

# Copy the manifests first so dependency download is cached independently of
# source changes: editing a handler does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs in a scratch/distroless image.
# -trimpath keeps build-host paths out of the binary; -s -w drop the symbol and
# DWARF tables, which are dead weight in a container.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/api \
    ./cmd/api

# ---

FROM gcr.io/distroless/static-nonroot:latest

WORKDIR /app
COPY --from=build /out/api /app/api

# Run unprivileged: nothing in this service needs root.
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/api"]
