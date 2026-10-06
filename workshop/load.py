#!/usr/bin/env python3
"""Finite synthetic requests to a local-only product journey; no dependencies."""
import argparse
import json
import time
import urllib.error
import urllib.parse
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--base-url", default="http://127.0.0.1:18080")
parser.add_argument("--rounds", type=int, default=60)
parser.add_argument("--interval", type=float, default=1.0)
parser.add_argument("--expect", choices=["healthy", "broken"], default="healthy")
args = parser.parse_args()
base = urllib.parse.urlparse(args.base_url)
if base.scheme != "http" or base.hostname not in {"127.0.0.1", "localhost", "::1"}:
    parser.error("base URL must be a local HTTP endpoint")
if not 1 <= args.rounds <= 600 or not 0 <= args.interval <= 30:
    parser.error("use 1-600 rounds and 0-30 seconds between rounds")
if base.username or base.password or base.query or base.fragment or base.path not in {"", "/"}:
    parser.error("base URL must contain only the local scheme, host and port")

# Both IDs and descriptions come from the public catalog's synthetic seed data.
products = [("OLJCESPC7Z", "Sunglasses"), ("0PUK6V6EV0", "Candle Holder")]
failures = 0
counts = {}
for round_number in range(args.rounds):
    for sku, name in products:
        path = f"/api/products/{sku}"
        start = time.monotonic()
        try:
            with urllib.request.urlopen(args.base_url.rstrip("/") + path, timeout=5) as response:
                status, body = response.status, response.read().decode()
        except urllib.error.HTTPError as error:
            status, body = error.code, error.read().decode()
        except urllib.error.URLError as error:
            status, body = 0, str(error.reason)
        expected = 404 if args.expect == "broken" and sku.startswith("0") else 200
        valid = status == expected
        if valid and status == 200:
            try:
                payload = json.loads(body)
                valid = payload["id"] == sku and payload["name"] == name
            except (ValueError, KeyError, TypeError):
                valid = False
        if valid and status == 404:
            valid = body.strip() == "Product not found"
        counts[str(status)] = counts.get(str(status), 0) + 1
        failures += int(not valid)
        print(json.dumps({"round": round_number + 1, "sku": sku, "status": status,
                          "duration_ms": round((time.monotonic() - start) * 1000, 2),
                          "matches_expectation": valid}), flush=True)
    if round_number + 1 < args.rounds:
        time.sleep(args.interval)
print(json.dumps({"summary": counts, "unexpected_results": failures}), flush=True)
raise SystemExit(1 if failures else 0)
