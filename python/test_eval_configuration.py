"""Offline configuration rendering only; never starts Docker resources."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class EvalConfigurationTest(unittest.TestCase):
    @unittest.skipUnless(shutil.which("docker"), "Docker CLI required for Compose rendering")
    def test_standalone_configuration_and_required_inputs(self):
        values = {
            "PATH": os.environ["PATH"],
            "HOME": os.environ["HOME"],
            "EVAL_PROJECT_NAME": "workload-701",
            "EVAL_OTLP_ENDPOINT": "collector.internal:443",
            "EVAL_INGESTION_KEY": "configuration-check-only",
            "EVAL_EXECUTE": "1",
            "EVAL_SCENARIO": "scoped-cpu-comparison-v1",
            "EVAL_RUN_ID": "private-701",
            "EVAL_REFERENCE_TIME": "2026-09-16T12:00:00Z",
        }
        command = ["docker", "compose", "--env-file", "/dev/null", "-f",
                   str(ROOT / "docker-compose.eval.yaml"), "config", "--format", "json"]
        result = subprocess.run(command, env=values, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(result.stdout)
        self.assertEqual(config["name"], "workload-701")
        self.assertTrue(config["networks"]["workload"]["internal"])
        for service in config["services"].values():
            for key in ("ports", "volumes", "container_name", "privileged", "network_mode"):
                self.assertFalse(service.get(key), key)
        workload = config["services"]["workload"]
        self.assertEqual(set(workload["networks"]), {"workload"})
        self.assertNotIn("EVAL_INGESTION_KEY", workload["environment"])
        self.assertEqual(workload["environment"]["RPS"], "0")
        self.assertNotIn("SIGNOZ_INGESTION_KEY", result.stdout)
        self.assertNotIn("ingest.us.signoz.cloud", result.stdout)
        for name in ("EVAL_OTLP_ENDPOINT", "EVAL_INGESTION_KEY", "EVAL_EXECUTE",
                     "EVAL_RUN_ID", "EVAL_REFERENCE_TIME", "EVAL_PROJECT_NAME"):
            missing = values.copy()
            del missing[name]
            check = subprocess.run(command, env=missing, capture_output=True, timeout=15)
            self.assertNotEqual(check.returncode, 0, name)

    def test_collector_has_only_explicit_eval_export(self):
        text = (ROOT / "otel-collector-eval-config.yaml").read_text()
        self.assertIn("${env:EVAL_OTLP_ENDPOINT}", text)
        self.assertIn("${env:EVAL_INGESTION_KEY}", text)
        self.assertIn("insecure: false", text)
        for unexpected in ("hostmetrics", "resourcedetection", "redis", "transform/", "/hostfs",
                           "${env:OTLP_ENDPOINT}", "${env:SIGNOZ_INGESTION_KEY}"):
            self.assertNotIn(unexpected, text)
        self.assertEqual(text.count("receivers: [otlp]"), 3)
        self.assertEqual(text.count("exporters: [otlp]"), 3)

    def test_startup_is_explicit_and_does_not_start_the_browser(self):
        result = subprocess.run(["bash", str(ROOT / "run-eval.sh")],
                                env={"PATH": os.environ["PATH"]}, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 2)
        text = (ROOT / "run-eval.sh").read_text()
        self.assertNotIn("browser-simulator.js", text)
        self.assertIn("--metrics_exporter none", text)
        self.assertIn("trap cleanup EXIT", text)


class EvalCleanupTest(unittest.TestCase):
    def run_cleanup(self, *, kind="javascript", child_status=0, original_status=0,
                    ignore_signals=False, early_exit=False):
        source = (ROOT / "run-eval.sh").read_text()
        cleanup = source[source.index("pids=()") : source.index("\ncd /app/javascript")]
        child = """
import os, signal, sys
def stop(signum, frame):
    sys.exit(int(os.environ["CHILD_STATUS"]))
handler = signal.SIG_IGN if os.environ["IGNORE_SIGNALS"] == "1" else stop
signal.signal(signal.SIGINT, handler)
signal.signal(signal.SIGTERM, handler)
print("ready", flush=True)
if os.environ["EARLY_EXIT"] == "1":
    sys.exit(int(os.environ["CHILD_STATUS"]))
while True:
    signal.pause()
"""
        script = "set -euo pipefail\n" + cleanup + """
# Scale only the test clock; production keeps its ten-second INT grace.
sleep() { command sleep 0.02; }
"$CHILD_PYTHON" -u -c "$CHILD_SCRIPT" > "$CHILD_READY" &
pids+=("$!")
pid_kinds+=("$CHILD_KIND")
for attempt in {1..100}; do
    [[ -s "$CHILD_READY" ]] && break
    command sleep 0.01
done
[[ -s "$CHILD_READY" ]] || exit 70
if [[ "$EARLY_EXIT" == 1 ]]; then
    for attempt in {1..100}; do
        kill -0 "${pids[0]}" 2>/dev/null || break
        command sleep 0.01
    done
fi
exit "$ORIGINAL_STATUS"
"""
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(
                ["bash", "-c", script], capture_output=True, text=True, timeout=5,
                env={
                    "PATH": os.environ["PATH"], "CHILD_PYTHON": sys.executable,
                    "CHILD_SCRIPT": child, "CHILD_READY": str(Path(directory) / "ready"),
                    "CHILD_KIND": kind, "CHILD_STATUS": str(child_status),
                    "ORIGINAL_STATUS": str(original_status),
                    "IGNORE_SIGNALS": "1" if ignore_signals else "0",
                    "EARLY_EXIT": "1" if early_exit else "0",
                },
            )
        return result

    def test_normal_children_and_expected_python_interrupt_succeed(self):
        for kind, status in (("javascript", 0), ("go", 0), ("python", 0), ("python", 130)):
            with self.subTest(kind=kind, status=status):
                result = self.run_cleanup(kind=kind, child_status=status)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_unexpected_shutdown_status_fails_successful_run(self):
        for kind, status in (("javascript", 1), ("javascript", 130), ("go", 130), ("python", 1)):
            with self.subTest(kind=kind, status=status):
                result = self.run_cleanup(kind=kind, child_status=status)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(f"exited with status {status}", result.stderr)

    def test_original_failure_survives_child_failure(self):
        result = self.run_cleanup(child_status=1, original_status=7)
        self.assertEqual(result.returncode, 7, result.stderr)

    def test_already_exited_child_status_is_reaped(self):
        result = self.run_cleanup(child_status=4, early_exit=True)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("exited with status 4", result.stderr)

    def test_stubborn_child_is_forced_to_stop_and_fails_the_run(self):
        result = self.run_cleanup(ignore_signals=True)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("exceeded the shutdown grace period", result.stderr)
        self.assertIn("exited with status 137", result.stderr)

    def test_cleanup_with_no_children_preserves_original_status(self):
        source = (ROOT / "run-eval.sh").read_text()
        cleanup = source[source.index("pids=()") : source.index("\ncd /app/javascript")]
        result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + cleanup + "\nexit 9"],
                                capture_output=True, text=True, timeout=2)
        self.assertEqual(result.returncode, 9, result.stderr)


if __name__ == "__main__":
    unittest.main()
