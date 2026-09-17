"""
Quote Service - Shipping quote calculation
"""
import logging
import os
from typing import Optional
from workload_mode import is_eval_mode, shipping_cost

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
        
        total_cost, handling_fee = shipping_cost(num_items)
        
        if handling_fee is not None:
            span.add_event("handling_fee_applied", {"fee": handling_fee})
            logger.info(f"Applied handling fee: ${handling_fee:.2f}")
        
        span.set_attribute("app.quote.cost.total", total_cost)
        
        quotes_counter.add(1, {"number_of_items": str(num_items)})
        quote_amount_histogram.record(total_cost)
        
        logger.info(f"Quote calculated: ${total_cost}")
        return {"cost_usd": total_cost, "items": num_items, "currency": "USD"}


def get_load_averages():
    """Get system load averages (1m, 5m, 15m)"""
    return os.getloadavg()


@app.on_event("startup")
async def startup_event():
    if is_eval_mode():
        logger.info(f"Quote Service starting on port {os.getenv('PORT', '8094')}")
        return
    SystemMetricsInstrumentor(config={
        "system.cpu.time": ["idle", "user", "system", "irq"],
        "system.cpu.utilization": ["idle", "user", "system", "irq"],
        "system.memory.usage": ["used", "free"],
        "system.memory.utilization": ["used", "free"],
        "system.swap.usage": None,
        "system.swap.utilization": None,
        "system.disk.io": None,
        "system.disk.operations": None,
        "system.disk.time": None,
        "system.network.io": None,
        "system.network.packets": None,
        "system.network.errors": None,
        "system.network.dropped_packets": None,
        "system.network.connections": None,
        "process.runtime.memory": ["rss", "vms"],
        "process.runtime.cpu.time": ["user", "system"],
        "process.runtime.cpython.context_switches": None,
    }).instrument()

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

    logger.info("Host metrics instrumentation started")
    logger.info(f"Quote Service starting on port {os.getenv('PORT', '8094')}")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=int(os.getenv("PORT", "8093")))
