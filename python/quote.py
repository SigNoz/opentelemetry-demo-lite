"""
Quote Service - Shipping quote calculation
"""
import logging
import os
import random
from typing import Optional

import psutil
from fastapi import FastAPI, Request
from pydantic import BaseModel
from opentelemetry import trace, metrics
from opentelemetry.metrics import Observation
from opentelemetry.instrumentation.system_metrics import SystemMetricsInstrumentor

logging.basicConfig(level=logging.INFO, format='%(asctime)s %(levelname)s [%(name)s] - %(message)s')
logger = logging.getLogger("quote")

app = FastAPI(title="Quote Service", version="1.0.0")

tracer = trace.get_tracer("quote")
meter = metrics.get_meter("quote")
quotes_counter = meter.create_counter("quotes", unit="{quotes}")
quote_amount_histogram = meter.create_histogram("quote.amount", unit="USD")


class QuoteRequest(BaseModel):
    numberOfItems: int = 1
    numberOfUnits: Optional[int] = None


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.post("/")
@app.post("/quote")
async def calculate_quote(request: Request, body: Optional[QuoteRequest] = None):
    current_span = trace.get_current_span()
    current_span.set_attributes({
        "rpc.system": "http",
        "rpc.service": "oteldemo.QuoteService",
        "rpc.method": "CalculateQuote",
    })
    
    logger.info("CalculateQuote request received")
    
    num_items = (body.numberOfItems or body.numberOfUnits or 1) if body else 1
    quote_result = calculate_shipping_quote(num_items)
    
    current_span.set_attribute("app.quote.cost.total", quote_result["cost_usd"])
    logger.info(f"Quote: ${quote_result['cost_usd']:.2f} for {num_items} items")
    
    return quote_result


def calculate_shipping_quote(num_items: int) -> dict:
    with tracer.start_as_current_span("calculate-quote", kind=trace.SpanKind.INTERNAL) as span:
        logger.info(f"Calculating quote for {num_items} items")
        
        span.set_attribute("app.quote.items.count", num_items)
        
        base_cost = 5.99
        per_item_cost = 1.50 + random.uniform(-0.25, 0.25)
        total_cost = base_cost + (num_items * per_item_cost)
        
        if random.random() < 0.2:
            handling_fee = random.uniform(1.0, 3.0)
            total_cost += handling_fee
            span.add_event("handling_fee_applied", {"fee": handling_fee})
            logger.info(f"Applied handling fee: ${handling_fee:.2f}")
        
        total_cost = round(total_cost, 2)
        span.set_attribute("app.quote.cost.total", total_cost)
        
        quotes_counter.add(1, {"number_of_items": str(num_items)})
        quote_amount_histogram.record(total_cost)
        
        logger.info(f"Quote calculated: ${total_cost}")
        return {"cost_usd": total_cost, "items": num_items, "currency": "USD"}


def get_load_averages():
    """Get system load averages (1m, 5m, 15m)"""
    return os.getloadavg()


def get_memory_stats():
    """Get memory stats with state attributes matching hostmetrics receiver format"""
    mem = psutil.virtual_memory()
    return [
        Observation(mem.used, {"state": "used"}),
        Observation(mem.free, {"state": "free"}),
        Observation(getattr(mem, 'cached', 0), {"state": "cached"}),
        Observation(getattr(mem, 'buffers', 0), {"state": "buffered"}),
    ]


@app.on_event("startup")
async def startup_event():
    # Start system metrics BUT exclude memory (we emit our own with correct states)
    SystemMetricsInstrumentor(config={
        "system.cpu.time": ["idle", "user", "system", "irq"],
        "system.cpu.utilization": ["idle", "user", "system", "irq"],
        # Exclude system.memory.usage - we emit our own below
        "process.runtime.memory": ["rss", "vms"],
        "process.runtime.cpu.time": ["user", "system"],
    }).instrument()

    # Create load average metrics - REQUIRED for ACTIVE status in SigNoz
    host_meter = metrics.get_meter("host-metrics")

    host_meter.create_observable_gauge(
        "system.cpu.load_average.1m",
        callbacks=[lambda options: [Observation(get_load_averages()[0])]],
        description="1-minute CPU load average",
        unit="1"
    )

    host_meter.create_observable_gauge(
        "system.cpu.load_average.5m",
        callbacks=[lambda options: [Observation(get_load_averages()[1])]],
        description="5-minute CPU load average",
        unit="1"
    )

    host_meter.create_observable_gauge(
        "system.cpu.load_average.15m",
        callbacks=[lambda options: [Observation(get_load_averages()[2])]],
        description="15-minute CPU load average",
        unit="1"
    )

    # system.memory.usage - with states matching hostmetrics receiver format
    # Using Gauge because SigNoz graph query auto-detection defaults to Unspecified temporality
    host_meter.create_observable_gauge(
        "system.memory.usage",
        callbacks=[lambda options: get_memory_stats()],
        description="Memory usage by state",
        unit="By"
    )

    logger.info("Host metrics instrumentation started")
    logger.info(f"Quote Service starting on port {os.getenv('PORT', '8094')}")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=int(os.getenv("PORT", "8093")))
