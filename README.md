# OpenTelemetry Demo Lite

Minimal multi-language microservices demo for OpenTelemetry. Simulates an e-commerce backend with Go, Node.js, and Python services emitting traces, metrics, and logs via OTLP.

## Prerequisites

- Docker and Docker Compose
- SigNoz Cloud account or self-hosted OTel Collector

## Quick Start

```bash
git clone https://github.com/SigNoz/opentelemetry-demo-lite.git
cd opentelemetry-demo-lite
```

### SigNoz Cloud

```bash
OTLP_ENDPOINT=ingest.<region>.signoz.cloud:443 SIGNOZ_INGESTION_KEY=<key> docker compose up -d
```

Or via `.env`:

```bash
OTLP_ENDPOINT=ingest.us.signoz.cloud:443
SIGNOZ_INGESTION_KEY=<your-ingestion-key>
```

### Self-hosted Collector

```bash
OTLP_ENDPOINT=<host>:4317 OTLP_INSECURE=true docker compose up -d
```

### Verify

```bash
docker compose ps -a
```

## Architecture

```
Browser Simulator (JS)
  → Frontend (JS)
      → Product Catalog (Go/SQLite)
      → Cart (Go/Redis)
      → Recommendation (Python)
      → Checkout (Go)
          → Payment (JS)
          → Shipping (Go) → Quote (Python)
          → Email (JS)
          → Accounting (Go)
          → Fraud Detection (Go)
```

## Services

| Service | Language | Port |
|---------|----------|------|
| Frontend | JS | 8080 |
| Payment | JS | 8081 |
| Shipping | Go | 8082 |
| Checkout | Go | 8083 |
| Cart | Go | 8084 |
| Product Catalog | Go | 8085 |
| Recommendation | Python | 8086 |
| Ad | JS | 8087 |
| Email | JS | 8088 |
| Currency | Go | 8089 |
| Browser Simulator | JS | 8090 |
| Accounting | Go | 8091 |
| Fraud Detection | Go | 8092 |
| Quote | Python | 8094 |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `OTLP_ENDPOINT` | `ingest.us.signoz.cloud:443` | OTel Collector endpoint |
| `SIGNOZ_INGESTION_KEY` | - | SigNoz Cloud ingestion key |
| `OTLP_INSECURE` | `false` | Use plain gRPC (no TLS) |
| `RPS` | `5` | Requests per second (0 = server-only mode) |

## Troubleshooting

### Enable Collector Debug Logs

```yaml
# otel-collector-config.yaml
service:
  telemetry:
    logs:
      level: debug
```

```bash
docker compose up -d --build
docker compose logs -f otel-col
```

### Inspect OTLP Export

Check for export errors in collector logs:

```bash
docker compose logs otel-col 2>&1 | grep -E "(error|failed|refused)"
```

Verify TLS handshake (SigNoz Cloud requires TLS):

```bash
openssl s_client -connect ingest.us.signoz.cloud:443 -servername ingest.us.signoz.cloud </dev/null
```

### Network Connectivity

Test endpoint reachability:

```bash
nc -zv <host> 4317                                    # TCP port check
curl -v telnet://ingest.us.signoz.cloud:443           # TLS endpoint check
```

Test OTLP/HTTP endpoint (if enabled on port 4318):

```bash
curl -X POST http://<host>:4318/v1/traces \
  -H "Content-Type: application/json" \
  -d '{"resourceSpans":[]}'
# Expected: {} or empty 200 response
```

### Container Issues

```bash
docker compose logs <service-name>
docker inspect <container-id> --format='{{.State.ExitCode}}'
docker stats --no-stream  # check memory/cpu
```

### Common Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| `connection refused` | Collector unreachable | Check `OTLP_ENDPOINT`, network/firewall |
| `certificate verify failed` | TLS mismatch | Set `OTLP_INSECURE=true` for non-TLS endpoints |
| `401 Unauthorized` | Invalid/missing key | Verify `SIGNOZ_INGESTION_KEY` |
| No data in SigNoz | Buffer delay | Wait 2-3 min, check collector logs |

## Local Development

Requires Go 1.21+, Node.js 18+, Python 3.11+.

```bash
./run.sh
```

Expects an OTel Collector on `localhost:4317`.

## Finite workload fixtures

The optional [fixture path](docs/finite-workloads.md) builds deterministic checkout and CPU
comparison inputs. It is dry-run-first and separate from the normal demo. Deployment, ingestion
and query-engine verification require a dedicated, explicitly approved destination.

## License

Apache 2.0
