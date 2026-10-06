# Finite workload fixtures

This opt-in path supports two worlds: `clean-checkout-v1` and `scoped-cpu-comparison-v1`.
It does not change the default demo startup. Neither world is a production readiness claim or a
replacement for an authorized end-to-end ingestion and query check.

## Offline inspection

From `go/`, this command prints a private manifest without starting services or exporters:

```sh
go run ./cmd/scenario --world scoped-cpu-comparison-v1 \
  --run-id inspection-701 --reference-time 2026-09-16T12:00:00Z
```

Use `--format otlp-json` or `--format otlp-proto` to inspect the metric payload instead. All formats
write to stdout. Keep the manifest and controller output private; do not ingest them as logs,
add them as telemetry attributes or expose them to the agent under evaluation.

The shared reference time must be explicit and at whole-second precision. The CPU window ends
one minute before that time and spans three minutes. It contains 18 gauge points plus 12 points
outside the window for boundary checks. Account, environment, cluster, service and task UUIDs
are ordinary resource attributes. Run IDs, world names and expected answers are not exported.
The gauges measure workload capacity units, not host CPU time; their unit is `1`.

Both worlds plan three checkout requests. Request trace IDs are retained in the private manifest
for later readback. The clean world emits no CPU comparison gauges. Runtime spans, logs and
generated order/transaction/tracking IDs remain genuine service output; they are not backdated
to the CPU window. Long historical workloads are not implemented here.

## Execution boundary

Do not run this path against an existing shared demo project or a production ingestion target.
`docker-compose.eval.yaml` is a standalone configuration, not an override to merge with the
default Compose file. It requires all of these explicit inputs:

- A unique `EVAL_PROJECT_NAME` and private `EVAL_RUN_ID` for each run.
- `EVAL_SCENARIO` and the shared `EVAL_REFERENCE_TIME`.
- `EVAL_OTLP_ENDPOINT` and a separately issued `EVAL_INGESTION_KEY` for an approved isolated target.
- `EVAL_EXECUTE=1` only after that run is authorized.

There are no published service ports or persistent data volumes. Redis uses ephemeral storage.
Only the collector joins the export network; application services stay on the internal network.
The collector exports with TLS and does not fall back to the ordinary demo destination or key.
Supply credentials through the execution environment, not committed files or shell history.

Run one world with the inputs above in the environment, then remove the project:

```sh
docker compose -f docker-compose.eval.yaml up --build --exit-code-from workload
docker compose -f docker-compose.eval.yaml down
```

`run-eval.sh` checks both execution gates before starting services, waits for bounded readiness,
runs the finite command and shuts down its own children. It discards the controller's manifest so
the expected answers never reach container logs; regenerate it with the offline command above and
the same world, run ID and reference time. `docker stop` interrupts a running world, and the
service's `stop_grace_period` leaves room for the script's ten-second shutdown grace. There is no
browser traffic generator. Only exact `EVAL_MODE=1` enables deterministic business inputs, disables
incidental host/periodic metrics and makes ignored dependency failures fail the workload. The CLI
itself also requires `--execute`; without it, it performs no network I/O. Direct CLI execution
requires local HTTP services and a local collector endpoint, not the external ingestion address.

An HTTP export acknowledgement does not prove storage or query fidelity. The manifest therefore
keeps `telemetryVerified: false`. Before a world is used for evaluation, independently verify all
core/control points, label/filter semantics, metric units, timestamp/bucket boundaries and the
expected traces/logs on the real target. Do not convert absent evidence into a passing baseline.

## Offline checks

```sh
(cd go && go test ./...)
node --test javascript/common/*.test.js
python3 -m unittest discover -s python -p 'test_*.py'
bash -n run-eval.sh
```

The configuration tests render Compose with deliberately fake settings; they do not start it.
The repository's release workflow publishes images, so it is not an offline validation command.

## Companion checkpoint and verification

This implementation was written for the assistant's Phase 6 eval work
([signoz-ai-assistant#452](https://github.com/SigNoz/signoz-ai-assistant/pull/452)). That PR merged
as Benchmark v1 without a Demo Lite consumer: its harness seeds its own background telemetry, so
no assistant eval currently runs these worlds. They supply finite workload inputs, not case
activation, human answer labels or a replacement for the assistant's evidence checks.

The 2026-09-17 publishing check passed `go test ./...`, `go vet ./...`, `go test -race ./...`,
36 Node tests, 17 Python tests, shell syntax and whitespace checks. Compose was rendered with
fake settings; no services or exporters were started. The default Compose file, startup script,
dependency manifests and release workflow are unchanged. No release workflow or image publication
was run, so these results are not a release-CI or current deployment claim.

The earlier authorized local Foundry proof recorded two clean checkout runs and two later,
nonoverlapping CPU runs against SigNoz v0.141.1. The retained results included 210 spans and
141 logs across 13 services per run, plus the CPU world's 18 core and 12 boundary-control points.
Earlier overlapping runs and failed bucketed queries remain negative evidence. The passing CPU
point-fidelity checks used a separate raw-query reference; they do not qualify arbitrary bucketed
queries, rewrite model queries or prove notification delivery. This historical proof was not
repeated for publishing, and its private raw artifacts are not part of this repository.

Opening the companion PR does not authorize deployment, new model trials, production access,
case activation or the separately planned assistant harness refactor.
