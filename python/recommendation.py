"""
Recommendation Service - Python FastAPI
"""
import logging
import os
from typing import List, Optional
from workload_mode import is_eval_mode, select_recommendations

from fastapi import FastAPI, Request
from opentelemetry import trace, metrics
from opentelemetry.metrics import Observation
from opentelemetry.instrumentation.system_metrics import SystemMetricsInstrumentor

logging.basicConfig(level=logging.INFO, format='%(asctime)s %(levelname)s [%(name)s] - %(message)s')
logger = logging.getLogger("recommendation")

app = FastAPI(title="Recommendation Service", version="1.0.0")

PRODUCTS = [
    {"id": "OLJCESPC7Z", "name": "Sunglasses", "categories": ["accessories"]},
    {"id": "66VCHSJNUP", "name": "Tank Top", "categories": ["clothing"]},
    {"id": "1YMWWN1N4O", "name": "Watch", "categories": ["accessories"]},
    {"id": "L9ECAV7KIM", "name": "Loafers", "categories": ["footwear"]},
    {"id": "2ZYFJ3GM2N", "name": "Hairdryer", "categories": ["beauty"]},
    {"id": "0PUK6V6EV0", "name": "Candle Holder", "categories": ["home"]},
    {"id": "LS4PSXUNUM", "name": "Salt Shaker", "categories": ["home"]},
    {"id": "9SIQT8TOJO", "name": "Bamboo Glass Jar", "categories": ["home"]},
    {"id": "6E92ZMYYFZ", "name": "Mug", "categories": ["home"]},
]

tracer = trace.get_tracer("recommendation")
meter = metrics.get_meter("recommendation")
recommendations_counter = meter.create_counter("app.recommendations.count", unit="{recommendations}")


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.get("/")
@app.get("/recommendations")
async def list_recommendations(request: Request, productIds: Optional[str] = None, user_id: Optional[str] = None):
    current_span = trace.get_current_span()
    current_span.set_attributes({
        "rpc.system": "grpc",
        "rpc.service": "oteldemo.RecommendationService",
        "rpc.method": "ListRecommendations",
    })
    
    logger.info("ListRecommendations request received")
    
    exclude_ids = [pid.strip() for pid in productIds.split(",")] if productIds else []
    recommendations = get_product_list(exclude_ids)
    
    current_span.set_attribute("app.recommendations.count", len(recommendations))
    logger.info(f"Generated {len(recommendations)} recommendations")
    
    return {"recommendations": [r["id"] for r in recommendations], "count": len(recommendations)}


def get_product_list(exclude_ids: List[str]) -> List[dict]:
    with tracer.start_as_current_span("get_product_list", kind=trace.SpanKind.INTERNAL) as span:
        logger.info(f"Filtering products, excluding {len(exclude_ids)} items")
        
        span.set_attribute("exclude.count", len(exclude_ids))
        available = [p for p in PRODUCTS if p["id"] not in exclude_ids]
        recommendations = select_recommendations(available)
        
        span.set_attribute("app.products.count", len(recommendations))
        span.add_event("recommendations_generated", {"count": len(recommendations)})
        recommendations_counter.add(1, {"products_excluded": str(len(exclude_ids))})
        
        logger.info(f"Selected {len(recommendations)} products")
        return recommendations


def get_load_averages():
    """Get system load averages (1m, 5m, 15m)"""
    return os.getloadavg()


@app.on_event("startup")
async def startup_event():
    if is_eval_mode():
        logger.info(f"Recommendation Service starting on port {os.getenv('PORT', '8086')}")
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
    logger.info(f"Recommendation Service starting on port {os.getenv('PORT', '8086')}")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=int(os.getenv("PORT", "8086")))
