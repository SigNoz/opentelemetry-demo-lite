#!/bin/bash
set -euo pipefail

if [[ "${EVAL_MODE:-}" != 1 || "${EVAL_EXECUTE:-}" != 1 ]]; then
    echo 'Explicit EVAL_MODE=1 and EVAL_EXECUTE=1 are required.' >&2
    exit 2
fi
: "${EVAL_SCENARIO:?Choose an approved world}"
: "${EVAL_RUN_ID:?Set a private run identifier}"
: "${EVAL_REFERENCE_TIME:?Set the shared UTC reference time}"

# Validate the entire plan before starting a process that can emit telemetry.
/app/bin/scenario --world "$EVAL_SCENARIO" --run-id "$EVAL_RUN_ID" \
    --reference-time "$EVAL_REFERENCE_TIME" >/dev/null

export RPS=0
export OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
export OTEL_EXPORTER_OTLP_ENDPOINT_HTTP=http://otel-collector:4318
export OTEL_METRICS_EXPORTER=none
export OTEL_PYTHON_DISABLED_INSTRUMENTATIONS=system_metrics
pids=()
pid_kinds=()
cleanup() {
    local status=$? pid index attempt active child_status forced=0
    trap - EXIT INT TERM
    for index in "${!pids[@]}"; do kill -INT "${pids[$index]}" 2>/dev/null || true; done
    for attempt in {1..10}; do
        active=0
        for index in "${!pids[@]}"; do kill -0 "${pids[$index]}" 2>/dev/null && active=1; done
        [[ "$active" == 0 ]] && break
        sleep 1
    done
    for index in "${!pids[@]}"; do
        pid="${pids[$index]}"
        if kill -0 "$pid" 2>/dev/null; then
            echo "${pid_kinds[$index]} process $pid exceeded the shutdown grace period." >&2
            forced=1
            [[ "$status" != 0 ]] || status=1
            kill -TERM "$pid" 2>/dev/null || true
        fi
    done
    if [[ "$forced" == 1 ]]; then
        sleep 1
        for index in "${!pids[@]}"; do
            pid="${pids[$index]}"
            if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
        done
    fi
    for index in "${!pids[@]}"; do
        pid="${pids[$index]}"
        child_status=0
        wait "$pid" || child_status=$?
        # Uvicorn re-raises SIGINT after graceful shutdown; JS and Go exit normally.
        if [[ "$child_status" != 0 && !( "${pid_kinds[$index]}" == python && "$child_status" == 130 ) ]]; then
            echo "${pid_kinds[$index]} process $pid exited with status $child_status." >&2
            [[ "$status" != 0 ]] || status=1
        fi
    done
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd /app/javascript
for service in frontend payment ad email; do
    OTEL_SERVICE_NAME="$service" node "$service.js" &
    pids+=("$!")
    pid_kinds+=(javascript)
done

cd /app/python
for spec in recommendation:8086 quote:8094; do
    module="${spec%:*}"
    port="${spec#*:}"
    OTEL_SERVICE_NAME="$module" \
    OTEL_RESOURCE_ATTRIBUTES='account=northstar-retail,deployment.environment=prod,cluster=main' \
    OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://otel-collector:4318/v1/logs \
    OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf \
    OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
    OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED=true \
    opentelemetry-instrument --traces_exporter otlp --metrics_exporter none --logs_exporter otlp \
        uvicorn "$module:app" --host 0.0.0.0 --port "$port" --log-level warning &
    pids+=("$!")
    pid_kinds+=(python)
done
/app/bin/go-services --service all &
pids+=("$!")
pid_kinds+=(go)

ready=0
for attempt in {1..30}; do
    ready=1
    for pid in "${pids[@]}"; do
        if ! kill -0 "$pid" 2>/dev/null; then echo 'A required process exited.' >&2; exit 1; fi
    done
    for port in 8080 8081 8082 8083 8084 8085 8086 8087 8088 8089 8091 8092 8094; do
        wget -q -T 1 -O /dev/null "http://127.0.0.1:$port/health" || ready=0
    done
    [[ "$ready" == 1 ]] && break
    sleep 1
done
if [[ "$ready" != 1 ]]; then echo 'Required service readiness timed out.' >&2; exit 1; fi

/app/bin/scenario --world "$EVAL_SCENARIO" --run-id "$EVAL_RUN_ID" \
    --reference-time "$EVAL_REFERENCE_TIME" --execute \
    --endpoint http://otel-collector:4318/v1/metrics
