/**
 * Browser Simulator - Mocks browser-side OpenTelemetry patterns
 */
const http = require('http');
const { initTelemetry, shutdown, emitLog, trace, propagation, context, SpanKind, SpanStatusCode } = require('./common/telemetry');

const PORT = process.env.PORT || 8090;
const FRONTEND_URL = process.env.FRONTEND_URL || 'http://localhost:8080';

const { tracer, meter, logger } = initTelemetry('browser-frontend');

// Metrics
const lcpHistogram = meter.createHistogram('lcp', { description: 'Largest Contentful Paint', unit: 'ms' });
const fcpHistogram = meter.createHistogram('fcp', { description: 'First Contentful Paint', unit: 'ms' });
const inpHistogram = meter.createHistogram('inp', { description: 'Interaction to Next Paint', unit: 'ms' });
const ttfbHistogram = meter.createHistogram('ttfb', { description: 'Time to First Byte', unit: 'ms' });
const clsGauge = meter.createObservableGauge('cls', { description: 'Cumulative Layout Shift' });
const userInteractionCounter = meter.createCounter('user_interactions', { unit: '{interactions}' });
const httpClientDuration = meter.createHistogram('http.client.request.duration', { unit: 's' });

let clsValue = 0;
let sessionId = `session-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;

clsGauge.addCallback((result) => {
    result.observe(clsValue, { 'url.path_template': '/home' });
});

function recordWebVitals(pageUrl = '/home') {
    const span = tracer.startSpan('web-vitals', { kind: SpanKind.INTERNAL });
    const ctx = trace.setSpan(context.active(), span);

    const attrs = { 'url.full': `https://shop.example.com${pageUrl}`, 'page.title': 'OTel Demo Shop' };

    context.with(ctx, () => {
        const lcp = Math.random() * 3000 + 500;
        const fcp = Math.random() * 2000 + 200;
        const ttfb = Math.random() * 1000 + 100;
        const inp = Math.random() * 400 + 50;
        clsValue = Math.random() * 0.2;

        span.setAttributes({ 'web_vital.lcp': lcp, 'web_vital.fcp': fcp, 'web_vital.ttfb': ttfb, 'web_vital.inp': inp, 'web_vital.cls': clsValue });
        lcpHistogram.record(lcp, attrs);
        fcpHistogram.record(fcp, attrs);
        ttfbHistogram.record(ttfb, attrs);
        inpHistogram.record(inp, attrs);

        emitLog(logger, `WebVitals recorded for ${pageUrl}`, { 'url.path': pageUrl });
    });

    span.end();
}

function simulateUserInteraction(eventType, elementId) {
    const span = tracer.startSpan(`${eventType} - ${elementId}`, {
        kind: SpanKind.INTERNAL,
        attributes: { 'event.type': eventType, 'target.id': elementId, 'session.id': sessionId }
    });
    const ctx = trace.setSpan(context.active(), span);

    context.with(ctx, () => {
        userInteractionCounter.add(1, { 'event.type': eventType });
        emitLog(logger, `User interaction: ${eventType} on ${elementId}`, { 'event.type': eventType, 'target.id': elementId });
    });

    span.end();
}

function simulateFetch(url, method = 'GET') {
    return new Promise((resolve) => {
        const startTime = Date.now();
        const parentContext = context.active();

        const span = tracer.startSpan(`HTTP ${method}`, {
            kind: SpanKind.CLIENT,
            attributes: {
                'http.request.method': method,
                'url.full': `${FRONTEND_URL}${url}`,
                'url.path': url,
                'server.address': 'localhost',
                'server.port': 8080
            }
        }, parentContext);

        const spanContext = trace.setSpan(parentContext, span);
        const headers = {};
        propagation.inject(spanContext, headers);

        context.with(spanContext, () => {
            emitLog(logger, `HTTP ${method} ${url}`, { 'http.method': method, 'url.path': url });
        });

        const parsedUrl = new URL(`${FRONTEND_URL}${url}`);
        const req = http.request({
            hostname: parsedUrl.hostname,
            port: parsedUrl.port || 8080,
            path: parsedUrl.pathname + parsedUrl.search,
            method,
            headers,
            timeout: 30000,
        }, (res) => {
            let data = '';
            res.on('data', (chunk) => { data += chunk; });
            res.on('end', () => {
                const duration = (Date.now() - startTime) / 1000;
                span.setAttributes({ 'http.response.status_code': res.statusCode });
                if (res.statusCode >= 400) {
                    span.setStatus({ code: SpanStatusCode.ERROR, message: `HTTP ${res.statusCode}` });
                }
                span.end();
                httpClientDuration.record(duration, { 'http.request.method': method });
                resolve(data);
            });
        });

        req.on('error', (err) => {
            context.with(spanContext, () => {
                emitLog(logger, `HTTP request failed: ${url}`, { 'error': err.message, 'url.path': url }, 'ERROR');
            });
            span.recordException(err);
            span.setStatus({ code: SpanStatusCode.ERROR, message: err.message });
            span.end();
            resolve(null);
        });

        req.on('timeout', () => {
            span.setStatus({ code: SpanStatusCode.ERROR, message: 'timeout' });
            span.end();
            req.destroy();
            resolve(null);
        });

        if (method === 'POST') {
            req.write(JSON.stringify({ user_id: 'demo-user', products: ['123', '456'] }));
        }
        req.end();
    });
}

function simulateError(errorType, message) {
    const span = tracer.startSpan(errorType, { kind: SpanKind.INTERNAL });
    const ctx = trace.setSpan(context.active(), span);

    context.with(ctx, () => {
        span.setAttributes({ 'exception.type': errorType, 'exception.message': message });
        span.setStatus({ code: SpanStatusCode.ERROR, message });
        span.recordException(new Error(message));
        emitLog(logger, `${errorType}: ${message}`, { 'exception.type': errorType }, 'ERROR');
    });

    span.end();
}

async function runSimulation(count = 5) {
    console.log(`\nBrowser Simulator - ${count} page views\n`);
    const pages = ['/home', '/products', '/products/123', '/cart', '/checkout'];

    for (let i = 0; i < count; i++) {
        const page = pages[i % pages.length];
        console.log(`Page View ${i + 1}: ${page}`);

        const pageSpan = tracer.startSpan('page-session', {
            kind: SpanKind.INTERNAL,
            attributes: { 'page.url': page, 'session.id': sessionId }
        });
        const sessionCtx = trace.setSpan(context.active(), pageSpan);

        await context.with(sessionCtx, async () => {
            recordWebVitals(page);
            await sleep(300);

            if (page === '/home') simulateUserInteraction('click', 'browse-products-btn');
            else if (page === '/products') simulateUserInteraction('click', 'product-card');
            else if (page === '/products/123') simulateUserInteraction('click', 'add-to-cart-btn');
            else if (page === '/cart') simulateUserInteraction('click', 'checkout-btn');
            else if (page === '/checkout') simulateUserInteraction('submit', 'checkout-form');
            await sleep(200);

            await simulateFetch('/api/products', 'GET');
            await simulateFetch('/api/cart?user_id=demo-user', 'GET');
            await simulateFetch('/api/recommendations?productIds=123,456', 'GET');
            await simulateFetch('/api/checkout', 'POST');

            if (Math.random() < 0.1) {
                simulateError('window.onerror', 'Cannot read property "price" of undefined');
            }
        });

        pageSpan.setStatus({ code: 0 });
        pageSpan.end();
        await sleep(500);
    }
    console.log('\nBrowser Simulation Complete!\n');
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function runSingleIteration() {
    const page = '/checkout';

    const pageSpan = tracer.startSpan('page-session', {
        kind: SpanKind.INTERNAL,
        attributes: { 'page.url': page, 'session.id': sessionId }
    });
    const pageCtx = trace.setSpan(context.active(), pageSpan);

    try {
        await context.with(pageCtx, async () => {
            // Web vitals as child span
            const webVitalsSpan = tracer.startSpan('web-vitals', {
                kind: SpanKind.INTERNAL,
                attributes: { 'url.path': page }
            });
            webVitalsSpan.setAttributes({
                'web_vital.lcp': Math.random() * 3000 + 500,
                'web_vital.fcp': Math.random() * 2000 + 200,
                'web_vital.ttfb': Math.random() * 1000 + 100,
                'web_vital.inp': Math.random() * 400 + 50,
                'web_vital.cls': Math.random() * 0.2
            });
            webVitalsSpan.end();

            // User interaction - checkout form submit
            const interactionSpan = tracer.startSpan('submit - checkout-form', {
                kind: SpanKind.INTERNAL,
                attributes: { 'event.type': 'submit', 'target.id': 'checkout-form' }
            });
            userInteractionCounter.add(1, { 'event.type': 'submit' });
            interactionSpan.end();

            await Promise.all([
                simulateFetch('/api/products', 'GET'),
                simulateFetch('/api/cart?user_id=demo-user', 'GET'),
                simulateFetch('/api/recommendations?productIds=123,456', 'GET'),
                simulateFetch('/api/ads', 'GET')
            ]);

            await simulateFetch('/api/checkout', 'POST');
        });

        pageSpan.setStatus({ code: 0 });
    } catch (err) {
        pageSpan.setStatus({ code: SpanStatusCode.ERROR, message: err.message });
    } finally {
        pageSpan.end();
    }

    return { success: true, page };
}


// HTTP Server for load testing
const server = http.createServer(async (req, res) => {
    if (req.url === '/health') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end('{"status":"ok"}');
        return;
    }
    if (req.url === '/trigger' || req.url === '/') {
        const result = await runSingleIteration();
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(result));
        return;
    }
    res.writeHead(404);
    res.end('Not found');
});

const rps = parseInt(process.env.RPS || '5');

server.listen(PORT, () => {
    console.log(`Browser Simulator listening on port ${PORT}`);
});

if (rps > 0) {
    const intervalMs = 1000 / rps;
    console.log(`Running continuously at ${rps} RPS (${intervalMs}ms interval)`);

    // Wait for services to start
    setTimeout(() => {
        setInterval(async () => {
            try {
                await runSingleIteration();
            } catch (err) {
                console.error('Iteration failed:', err.message);
            }
        }, intervalMs);
    }, 5000); // 5s startup delay
} else {
    console.log('RPS=0: HTTP server only (trigger via /trigger endpoint)');
}

process.on('SIGINT', async () => {
    console.log('Shutting down');
    await shutdown();
    process.exit(0);
});
