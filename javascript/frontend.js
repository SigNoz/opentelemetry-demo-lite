/**
 * Frontend Service - Routes to backend services
 */
const http = require('http');
const url = require('url');
const { isEvalMode } = require('./common/workload-mode');
const { initTelemetry, registerShutdownHandler, emitLog, trace, propagation, context, SpanKind } = require('./common/telemetry');

const PORT = process.env.PORT || 8080;
const { tracer, meter, logger } = initTelemetry('frontend');

const requestCounter = meter.createCounter('app.frontend.requests', { unit: '{requests}' });

const SERVICES = {
    checkout: process.env.CHECKOUT_URL || 'http://localhost:8083',
    cart: process.env.CART_URL || 'http://localhost:8084',
    productCatalog: process.env.PRODUCT_CATALOG_URL || 'http://localhost:8085',
    recommendation: process.env.RECOMMENDATION_URL || 'http://localhost:8086',
    ad: process.env.AD_URL || 'http://localhost:8087',
    payment: process.env.PAYMENT_URL || 'http://localhost:8081',
    shipping: process.env.SHIPPING_URL || 'http://localhost:8082',
    email: process.env.EMAIL_URL || 'http://localhost:8088',
    currency: process.env.CURRENCY_URL || 'http://localhost:8089',
};

const server = http.createServer(async (req, res) => {
    const parsedUrl = url.parse(req.url, true);
    const path = parsedUrl.pathname;

    if (path === '/health') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end('{"status":"ok"}');
        return;
    }

    const ctx = propagation.extract(context.active(), req.headers);
    const span = tracer.startSpan(`${req.method} ${path}`, {
        kind: SpanKind.SERVER,
        attributes: { 'http.method': req.method, 'http.url': req.url, 'http.route': path },
    }, ctx);

    requestCounter.add(1, { route: path, method: req.method });

    try {
        await context.with(trace.setSpan(ctx, span), async () => {
            emitLog(logger, `Incoming request: ${req.method} ${path}`, { 'http.method': req.method, 'http.route': path });

            if (path === '/' || path === '/index.html') {
                res.writeHead(200, { 'Content-Type': 'text/html' });
                res.end('<html><body><h1>OTel Demo Frontend</h1></body></html>');
            } else if (path === '/api/products') {
                emitLog(logger, 'Fetching products from catalog');
                const response = await makeRequest('GET', `${SERVICES.productCatalog}/products`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path.startsWith('/api/products/')) {
                const productId = path.replace('/api/products/', '');
                emitLog(logger, `Fetching product ${productId}`, { 'product.id': productId });
                const response = await makeRequest('GET', `${SERVICES.productCatalog}/products/${productId}`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path === '/api/cart') {
                const userId = parsedUrl.query.user_id || 'anonymous';
                emitLog(logger, `Getting cart for user ${userId}`, { 'user.id': userId });
                const response = await makeRequest('GET', `${SERVICES.cart}/cart?user_id=${userId}`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path === '/api/recommendations') {
                const productIds = parsedUrl.query.productIds || '';
                emitLog(logger, 'Fetching recommendations', { 'product.ids': productIds });
                const response = await makeRequest('GET', `${SERVICES.recommendation}/?productIds=${productIds}`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path === '/api/checkout') {
                emitLog(logger, 'Processing checkout request');
                const response = await makeRequest('POST', `${SERVICES.checkout}/checkout`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path === '/api/ads') {
                emitLog(logger, 'Fetching ads');
                const response = await makeRequest('GET', `${SERVICES.ad}/ads`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else if (path === '/api/currencies') {
                emitLog(logger, 'Fetching currencies');
                const response = await makeRequest('GET', `${SERVICES.currency}/currencies`, span);
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(response);
            } else {
                res.writeHead(404);
                res.end('{"error":"Not found"}');
            }
        });
        span.setAttribute('http.status_code', res.statusCode);
    } catch (err) {
        context.with(trace.setSpan(ctx, span), () => {
            emitLog(logger, `Request failed: ${err.message}`, { 'error': err.message }, 'ERROR');
        });
        span.recordException(err);
        span.setStatus({ code: 2, message: err.message });
        res.writeHead(500);
        res.end(JSON.stringify({ error: err.message }));
    } finally {
        span.end();
    }
});

function makeRequest(method, urlString, parentSpan) {
    return new Promise((resolve, reject) => {
        const parsedUrl = url.parse(urlString);
        const parentContext = trace.setSpan(context.active(), parentSpan);

        const clientSpan = tracer.startSpan(`HTTP ${method}`, { kind: SpanKind.CLIENT }, parentContext);
        clientSpan.setAttributes({ 'http.method': method, 'http.url': urlString });

        const clientContext = trace.setSpan(parentContext, clientSpan);
        const headers = {};
        propagation.inject(clientContext, headers);

        const req = http.request({
            hostname: parsedUrl.hostname,
            port: parsedUrl.port,
            path: parsedUrl.path,
            method,
            headers,
            timeout: 30000,
        }, (res) => {
            let data = '';
            res.on('data', (chunk) => data += chunk);
            res.on('end', () => {
                clientSpan.setAttribute('http.status_code', res.statusCode);
                clientSpan.end();
                if (isEvalMode() && res.statusCode !== 200) {
                    reject(new Error(`Backend returned HTTP ${res.statusCode}`));
                    return;
                }
                resolve(data || '{}');
            });
        });

        req.on('error', (err) => {
            clientSpan.recordException(err);
            clientSpan.setStatus({ code: 2, message: err.message });
            clientSpan.end();
            if (isEvalMode()) { reject(err); return; }
            resolve(JSON.stringify({ error: err.message }));
        });

        req.on('timeout', () => {
            req.destroy();
            clientSpan.setStatus({ code: 2, message: 'timeout' });
            clientSpan.end();
            if (isEvalMode()) { reject(new Error('timeout')); return; }
            resolve('{"error":"timeout"}');
        });

        req.end();
    });
}

server.listen(PORT, () => console.log(`Frontend Service listening on port ${PORT}`));
registerShutdownHandler();
