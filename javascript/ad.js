/**
 * Ad Service - Ad serving
 */
const http = require('http');
const url = require('url');
const { isEvalMode, fallbackAds } = require('./common/workload-mode');
const { initTelemetry, registerShutdownHandler, emitLog, trace, propagation, context, SpanKind } = require('./common/telemetry');

const PORT = process.env.PORT || 8087;
const { tracer, meter, logger } = initTelemetry('ad');

const adRequestsCounter = meter.createCounter('app.ads.ad_requests', { unit: '{requests}' });

const ADS_BY_CATEGORY = {
    accessories: [{ id: 'ad-1', text: 'Premium Sunglasses - 50% off!', url: '/products/OLJCESPC7Z' }],
    clothing: [{ id: 'ad-3', text: 'Summer Tank Tops', url: '/products/66VCHSJNUP' }],
    footwear: [{ id: 'ad-4', text: 'Designer Loafers', url: '/products/L9ECAV7KIM' }],
    home: [{ id: 'ad-5', text: 'Home Decor Essentials', url: '/products/0PUK6V6EV0' }],
    default: [{ id: 'ad-8', text: 'Shop Our Best Sellers!', url: '/' }],
};

const server = http.createServer((req, res) => {
    const parsedUrl = url.parse(req.url, true);
    const ctx = propagation.extract(context.active(), req.headers);

    if (isEvalMode() && parsedUrl.pathname === '/health' && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end('{"status":"ok"}');
    } else if (parsedUrl.pathname === '/ads' || parsedUrl.pathname === '/') {
        handleGetAds(req, res, ctx, parsedUrl.query);
    } else {
        res.writeHead(404);
        res.end('{"error":"Not found"}');
    }
});

function handleGetAds(req, res, parentCtx, query) {
    const span = tracer.startSpan('getAds', { kind: SpanKind.SERVER }, parentCtx);

    context.with(trace.setSpan(parentCtx, span), () => {
        try {
            const contextKeys = query.context_keys ? query.context_keys.split(',') : [];
            span.setAttribute('app.ads.contextKeys', contextKeys.join(','));
            span.setAttribute('app.ads.contextKeys.count', contextKeys.length);

            // Read session from baggage
            const baggage = propagation.getBaggage(trace.setSpan(parentCtx, span));
            if (baggage) {
                const sessionId = baggage.getEntry('session.id');
                if (sessionId) span.setAttribute('session.id', sessionId.value);
            }

            let ads, requestType, responseType;
            const category = query.category || (contextKeys.length > 0 ? contextKeys[0] : null);

            if (category && ADS_BY_CATEGORY[category]) {
                ads = ADS_BY_CATEGORY[category];
                requestType = 'TARGETED';
                responseType = 'TARGETED';
            } else {
                const allAds = Object.values(ADS_BY_CATEGORY).flat();
                ads = fallbackAds(allAds);
                requestType = 'NOT_TARGETED';
                responseType = 'RANDOM';
            }

            span.setAttributes({
                'app.ads.ad_request_type': requestType,
                'app.ads.ad_response_type': responseType,
                'app.ads.count': ads.length,
            });

            adRequestsCounter.add(1, { ad_request_type: requestType, ad_response_type: responseType });
            emitLog(logger, `Served ${ads.length} ${responseType.toLowerCase()} ads`, { 'ads.count': ads.length, 'ads.type': responseType });

            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ ads }));
            span.end();
        } catch (err) {
            span.recordException(err);
            span.setStatus({ code: 2, message: err.message });
            span.end();
            res.writeHead(500);
            res.end(JSON.stringify({ error: err.message }));
        }
    });
}

server.listen(PORT, () => console.log(`Ad Service listening on port ${PORT}`));
registerShutdownHandler();
