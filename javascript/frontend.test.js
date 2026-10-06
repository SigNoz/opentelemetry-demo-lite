const assert = require('node:assert/strict');
const http = require('node:http');
const { test } = require('node:test');
const { once } = require('node:events');
const { NodeTracerProvider } = require('@opentelemetry/sdk-trace-node');
const { InMemorySpanExporter, SimpleSpanProcessor } = require('@opentelemetry/sdk-trace-base');
const { MeterProvider } = require('@opentelemetry/sdk-metrics');
const { Resource } = require('@opentelemetry/resources');
const { createFrontendServer } = require('./frontend');

test('frontend propagates catalog results and correlates failed client spans with logs', async (t) => {
    const exporter = new InMemorySpanExporter();
    const provider = new NodeTracerProvider({ resource: new Resource({ 'service.name': 'frontend' }) });
    provider.addSpanProcessor(new SimpleSpanProcessor(exporter));
    provider.register();
    const meterProvider = new MeterProvider();
    const logs = [];
    const telemetry = {
        tracer: provider.getTracer('frontend'),
        meter: meterProvider.getMeter('frontend'),
        logger: { emit: (record) => logs.push(record) },
    };
    const backend = http.createServer((req, res) => {
        const status = req.url.endsWith('not-found') ? 404 : req.url.endsWith('db-error') ? 500 : 200;
        res.writeHead(status, { 'Content-Type': status === 200 ? 'application/json' : 'text/plain' });
        res.end(status === 200 ? '{"id":"OLJCESPC7Z"}' : status === 404 ? 'Product not found\n' : 'Database error\n');
    });
    backend.listen(0, '127.0.0.1');
    await once(backend, 'listening');
    const frontend = createFrontendServer(telemetry, { productCatalog: `http://127.0.0.1:${backend.address().port}` });
    frontend.listen(0, '127.0.0.1');
    await once(frontend, 'listening');
    t.after(async () => {
        await new Promise(resolve => frontend.close(resolve));
        await new Promise(resolve => backend.close(resolve));
        await provider.shutdown();
        await meterProvider.shutdown();
    });
    for (const [id, expected] of [['OLJCESPC7Z', 200], ['not-found', 404], ['db-error', 500]]) {
        const response = await fetch(`http://127.0.0.1:${frontend.address().port}/api/products/${id}`);
        assert.equal(response.status, expected);
        assert.match(response.headers.get('content-type'), expected === 200 ? /json/ : /text\/plain/);
        const body = await response.text();
        assert.equal(body, expected === 200 ? '{"id":"OLJCESPC7Z"}' : expected === 404 ? 'Product not found\n' : 'Database error\n');
    }
    const liveness = await fetch(`http://127.0.0.1:${frontend.address().port}/health`);
    assert.equal(liveness.status, 200);
    await liveness.text();
    await provider.forceFlush();
    const spans = exporter.getFinishedSpans();
    const clients = spans.filter(span => span.name === 'HTTP GET');
    const servers = spans.filter(span => span.name === 'GET /api/products/{id}');
    assert.equal(clients.length, 3);
    assert.equal(servers.length, 3);
    assert.deepEqual(clients.map(span => span.status.code), [0, 2, 2]);
    assert.deepEqual(servers.map(span => span.status.code), [0, 0, 2]);
    const upstreamLogs = logs.filter(log => log.body === 'Upstream request failed');
    assert.deepEqual(upstreamLogs.map(log => log.severityText), ['WARN', 'ERROR']);
    const { trace } = require('@opentelemetry/api');
    for (const record of upstreamLogs) {
        const sc = trace.getSpan(record.context).spanContext();
        assert.ok(clients.some(span => span.spanContext().spanId === sc.spanId && span.spanContext().traceId === sc.traceId));
    }
});
