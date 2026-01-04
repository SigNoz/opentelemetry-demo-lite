# Stage 1: Build Go binary (with CGO for SQLite)
FROM golang:1.23-alpine AS go-builder
WORKDIR /build
# Install build dependencies for CGO and SQLite
RUN apk add --no-cache gcc musl-dev
COPY go/go.mod go/go.sum ./
COPY go/ ./
# Update go.sum with new dependencies and download
RUN go mod tidy && go mod download
# CGO_ENABLED=1 required for go-sqlite3
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-w -s" \
    -o /go-services .

FROM node:20-alpine AS js-builder
WORKDIR /build
COPY javascript/package.json ./
RUN npm install --omit=dev --silent && \
    npm cache clean --force

FROM alpine:3.19

RUN apk add --no-cache \
    nodejs \
    npm \
    python3 \
    py3-pip \
    bash \
    ca-certificates \
    && rm -rf /var/cache/apk/*

WORKDIR /app

COPY --from=go-builder /go-services /app/bin/go-services

COPY python/requirements.txt /tmp/requirements.txt
RUN apk add --no-cache --virtual .build-deps gcc musl-dev python3-dev linux-headers && \
    pip install --no-cache-dir --break-system-packages -r /tmp/requirements.txt && \
    opentelemetry-bootstrap --action=install && \
    rm /tmp/requirements.txt && \
    apk del .build-deps
COPY python/ /app/python/

COPY --from=js-builder /build/node_modules /app/javascript/node_modules
COPY javascript/ /app/javascript/

COPY run-docker.sh /app/run-docker.sh
RUN chmod +x /app/run-docker.sh

ENV RPS=5
ENV OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
ENV OTEL_EXPORTER_OTLP_INSECURE=true

EXPOSE 8080 8081 8082 8083 8084 8085 8086 8087 8088 8089 8090 8091 8092 8093 8094

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q --spider http://localhost:8080/health || exit 1

CMD ["/app/run-docker.sh"]
