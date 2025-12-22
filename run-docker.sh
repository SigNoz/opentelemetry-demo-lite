#!/bin/bash

set -e

COUNT=${COUNT:-10}

echo "========================================"
echo "OTel Demo Mock - Docker Container"
echo "========================================"
echo "Count: $COUNT"
echo ""

echo "Starting JavaScript services..."
cd /app/javascript

OTEL_SERVICE_NAME=frontend node frontend.js &
sleep 0.5
OTEL_SERVICE_NAME=payment node payment.js &
sleep 0.5
OTEL_SERVICE_NAME=ad node ad.js &
sleep 0.5
OTEL_SERVICE_NAME=email node email.js &
sleep 0.5

echo "Starting Python services..."
cd /app/python

OTEL_SERVICE_NAME=recommendation \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://otel-collector:4318/v1/logs \
OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED=true \
opentelemetry-instrument \
    --traces_exporter otlp \
    --metrics_exporter otlp \
    --logs_exporter otlp \
    uvicorn recommendation:app --host 0.0.0.0 --port 8086 &
sleep 0.3

OTEL_SERVICE_NAME=quote-python \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://otel-collector:4318/v1/logs \
OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED=true \
opentelemetry-instrument \
    --traces_exporter otlp \
    --metrics_exporter otlp \
    --logs_exporter otlp \
    uvicorn quote:app --host 0.0.0.0 --port 8094 &
sleep 0.3

echo "Starting Browser Simulator..."
cd /app/javascript
OTEL_SERVICE_NAME=browser-frontend BROWSER_COUNT=${COUNT} node browser-simulator.js &
sleep 0.5

echo ""
echo "========================================"
echo "All services started!"
echo "========================================"
echo ""
echo "Multi-Language Trace Flow:"
echo "  Browser-Simulator (JS)"
echo "    → Frontend (JS)"
echo "        → Product-Catalog (Go)"
echo "        → Cart (Go) → Redis"
echo "        → Recommendation (Python)  ← Python auto-instrumented"
echo "        → Checkout (Go)"
echo "            → Payment (JS)"
echo "            → Shipping (Go)"
echo "                → Quote (Python)   ← Python auto-instrumented"
echo "            → Email (JS)"
echo "            → Kafka → Accounting + Fraud-Detection (Go)"
echo ""

/app/bin/go-services --service all --count 0 &

if [ "$COUNT" = "0" ]; then
    echo ""
    echo "========================================="
    echo "Services running in LOAD TEST MODE"
    echo "========================================="
    echo "All endpoints ready for external load testing (k6, etc.)"
    echo "Services will keep running until container is stopped."
    echo ""
    echo "Available endpoints:"
    echo "  - http://localhost:8080/api/products"
    echo "  - http://localhost:8080/api/cart"
    echo "  - http://localhost:8080/api/recommendations"
    echo "  - http://localhost:8080/api/checkout"
    echo ""
    echo "Press Ctrl+C or 'docker-compose down' to stop."
    echo "========================================="
    
    # Keep container running indefinitely
    tail -f /dev/null
else
    # Wait for browser simulation to complete
    sleep $((COUNT * 2 + 10))
    
    echo ""
    echo "========================================"
    echo "Demo completed! $COUNT traces generated."
    echo "========================================"

    sleep 120
    echo "Press Ctrl+C or 'docker-compose down' to stop."
    # Keep container running indefinitely
    tail -f /dev/null
fi
