# Build stage
FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=1 go build \
    -tags "fts5" \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/daimon ./cmd/daimon

# Runtime stage — debian slim required for CGO (SQLite)
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -r -s /bin/false daimon

WORKDIR /app

COPY --from=builder /out/daimon /app/daimon
COPY configs/daimon.example.yaml /app/configs/daimon.example.yaml
COPY configs/daimon.example.yaml /app/configs/daimon.yaml

RUN mkdir -p /app/data /home/daimon/.daimon && \
    chown -R daimon:daimon /app /home/daimon

USER daimon

EXPOSE 8080 9090 9191

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD /app/daimon version || exit 1

# OCI labels
LABEL org.opencontainers.image.title="Daimon"
LABEL org.opencontainers.image.description="Local-first, self-evolving AI agent runtime"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${COMMIT}"
LABEL org.opencontainers.image.created="${DATE}"

ENTRYPOINT ["/app/daimon"]
CMD ["start", "--config", "/app/configs/daimon.yaml"]
