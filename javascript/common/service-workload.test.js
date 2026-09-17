const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

function service(name, enabled, shutdown = () => Promise.resolve(), backend = {}) {
    const observed = { exits: [], errors: [], signals: {}, spans: [], outbound: [], destroyed: false };
    const fakeProcess = {
        env: enabled ? { EVAL_MODE: '1' } : {},
        on(signal, callback) { observed.signals[signal] = callback; },
        exit(code) { observed.exits.push(code); },
    };
    const workload = { module: { exports: {} }, process: fakeProcess, Math: Object.create(Math) };
    workload.Math.random = () => 0;
    vm.runInNewContext(readFileSync(path.join(__dirname, 'workload-mode.js'), 'utf8'), workload);
    const sharedTelemetry = {
        module: { exports: {} }, process: fakeProcess,
        console: { error(...args) { observed.errors.push(args); } },
        require: name => name === './workload-mode' ? workload.module.exports : {},
    };
    vm.runInNewContext(readFileSync(path.join(__dirname, 'telemetry.js'), 'utf8'), sharedTelemetry);
    const telemetry = {
        initTelemetry() {
            return {
                tracer: {
                    startSpan() {
                        const span = {
                            attributes: {}, exceptions: [],
                            setAttributes(attributes) { Object.assign(this.attributes, attributes); },
                            setAttribute(key, value) { this.attributes[key] = value; },
                            recordException(error) { this.exceptions.push(error); },
                            setStatus() {}, addEvent() {}, end() {},
                        };
                        observed.spans.push(span);
                        return span;
                    },
                },
                meter: { createCounter: () => ({ add() {} }), createHistogram: () => ({ record() {} }) },
            };
        },
        registerShutdownHandler: () => sharedTelemetry.module.exports.registerShutdownHandler(shutdown),
        emitLog() {},
        trace: { setSpan: ctx => ctx },
        propagation: { extract: ctx => ctx, getBaggage() {}, inject() {} },
        context: { active: () => ({}), with: (ctx, callback) => callback() },
        SpanKind: { SERVER: 1 }, SpanStatusCode: { ERROR: 2 },
    };
    const imports = {
        http: {
            createServer(handler) { observed.handler = handler; return { listen() {} }; },
            request(options, callback) {
                observed.outbound.push(options);
                const request = new EventEmitter();
                request.destroy = () => { observed.destroyed = true; };
                request.end = () => {
                    if (backend.failure === 'network') {
                        request.emit('error', new Error('connect ECONNREFUSED'));
                    } else if (backend.failure === 'timeout') {
                        request.emit('timeout');
                    } else {
                        const response = new EventEmitter();
                        response.statusCode = backend.statusCode ?? 200;
                        callback(response);
                        response.emit('data', backend.body ?? '{"status":"order_placed"}');
                        response.emit('end');
                    }
                };
                return request;
            },
        },
        url: require('node:url'),
        crypto: { randomUUID: () => '11111111-2222-3333-4444-555555555555' },
        './common/workload-mode': workload.module.exports,
        './common/telemetry': telemetry,
    };
    vm.runInNewContext(readFileSync(path.join(__dirname, '..', `${name}.js`), 'utf8'), {
        process: fakeProcess,
        console: { log() {}, error(...args) { observed.errors.push(args); } },
        require(name) {
            assert.ok(Object.hasOwn(imports, name), `Unexpected import: ${name}`);
            return imports[name];
        },
    });
    observed.request = (url, method = 'GET') => {
        const result = {};
        observed.handler({ url, method, headers: {} }, {
            writeHead(status) { result.status = status; },
            end(body) { result.body = JSON.parse(body); },
        });
        return result;
    };
    observed.requestAsync = async (url, method = 'GET') => {
        const result = {};
        await observed.handler({ url, method, headers: {} }, {
            writeHead(status) { result.status = status; this.statusCode = status; },
            end(body) { result.body = JSON.parse(body); },
        });
        return result;
    };
    return observed;
}

for (const name of ['payment', 'email', 'ad']) {
    test(`${name} exposes health only in eval mode`, () => {
        assert.equal(service(name, true).request('/health').status, 200);
        assert.equal(service(name, true).request('/health', 'POST').status, 404);
        assert.equal(service(name, false).request('/health').status, 404);
    });

}

for (const name of ['payment', 'email', 'ad', 'frontend']) {
    test(`${name} awaits telemetry shutdown before exiting in eval mode`, async () => {
        let finish;
        const pending = new Promise(resolve => { finish = resolve; });
        const app = service(name, true, () => pending);
        const stopping = app.signals.SIGINT();
        assert.deepEqual(app.exits, []);
        finish();
        await stopping;
        assert.deepEqual(app.exits, [0]);
    });

    test(`${name} retains immediate normal-mode exit`, async () => {
        const app = service(name, false, () => new Promise(() => {}));
        await app.signals.SIGINT();
        assert.deepEqual(app.exits, [0]);
    });

    test(`${name} reports failed shutdown and exits nonzero in eval mode`, async () => {
        const app = service(name, true, () => Promise.reject(new Error('flush failed')));
        await app.signals.SIGINT();
        assert.deepEqual(app.exits, [1]);
        assert.equal(app.errors.length, 1);
    });
}

test('repeated eval SIGINT does not start another telemetry shutdown', async () => {
    let calls = 0;
    let finish;
    const pending = new Promise(resolve => { finish = resolve; });
    const app = service('frontend', true, () => { calls += 1; return pending; });
    const stopping = app.signals.SIGINT();
    await app.signals.SIGINT();
    assert.equal(calls, 1);
    assert.deepEqual(app.exits, []);
    finish();
    await stopping;
    assert.deepEqual(app.exits, [0]);
});

test('payment request retains its normal failure branch and succeeds in eval mode', () => {
    const normal = service('payment', false);
    assert.equal(normal.request('/charge', 'POST').status, 402);
    assert.equal(normal.spans[0].exceptions[0].message, 'Payment failed: insufficient funds');
    const stable = service('payment', true);
    const response = stable.request('/charge', 'POST');
    assert.equal(response.status, 200);
    assert.equal(response.body.amount, '110.00');
    assert.equal(response.body.currency, 'USD');
    assert.equal(stable.spans[0].attributes['app.loyalty.level'], 'silver');
    assert.equal(stable.spans[0].exceptions.length, 0);
});

test('email request retains its normal failure branch and request overrides in eval mode', () => {
    const normal = service('email', false);
    assert.equal(normal.request('/send', 'POST').status, 500);
    assert.equal(normal.spans[0].exceptions[0].message, 'SMTP connection failed');
    const stable = service('email', true);
    const fallback = stable.request('/send', 'POST');
    assert.equal(fallback.status, 200);
    assert.equal(fallback.body.recipient, 'user-1042@example.com');
    const supplied = stable.request('/send?user_id=user-24&order_id=order-123&email=customer@example.com', 'POST');
    assert.equal(supplied.status, 200);
    assert.equal(supplied.body.recipient, 'customer@example.com');
    assert.equal(supplied.body.order_id, 'order-123');
    assert.equal(stable.spans[1].attributes['app.user.id'], 'user-24');
});

test('ad requests keep targeting and use stable fallback without changing the catalog', () => {
    const stable = service('ad', true);
    assert.equal(stable.request('/ads').body.ads[0].id, 'ad-1');
    assert.equal(stable.request('/ads?category=clothing').body.ads[0].id, 'ad-3');
    assert.equal(stable.request('/ads').body.ads[0].id, 'ad-1');
});

for (const enabled of [false, true]) {
    const mode = enabled ? 'eval' : 'normal';
    test(`frontend checkout succeeds through the backend in ${mode} mode`, async () => {
        const app = service('frontend', enabled);
        const response = await app.requestAsync('/api/checkout', 'POST');
        assert.deepEqual(response, { status: 200, body: { status: 'order_placed' } });
        assert.equal(app.outbound.length, 1);
        assert.equal(app.outbound[0].hostname, 'localhost');
        assert.equal(app.outbound[0].port, '8083');
        assert.equal(app.outbound[0].path, '/checkout');
        assert.equal(app.outbound[0].method, 'POST');
        assert.equal(app.spans[0].attributes['http.status_code'], 200);
        assert.equal(app.destroyed, false);
    });

    test(`frontend handles backend non-200 responses in ${mode} mode`, async () => {
        const app = service('frontend', enabled, undefined, {
            statusCode: 503, body: '{"error":"backend unavailable"}',
        });
        const response = await app.requestAsync('/api/checkout', 'POST');
        assert.equal(response.status, enabled ? 500 : 200);
        assert.deepEqual(response.body, {
            error: enabled ? 'Backend returned HTTP 503' : 'backend unavailable',
        });
        assert.equal(app.spans[0].exceptions.length, enabled ? 1 : 0);
        assert.equal(app.spans[1].attributes['http.status_code'], 503);
        assert.equal(app.outbound.length, 1);
    });

    test(`frontend handles backend network failures in ${mode} mode`, async () => {
        const app = service('frontend', enabled, undefined, { failure: 'network' });
        const response = await app.requestAsync('/api/checkout', 'POST');
        assert.equal(response.status, enabled ? 500 : 200);
        assert.deepEqual(response.body, { error: 'connect ECONNREFUSED' });
        assert.equal(app.spans[0].exceptions.length, enabled ? 1 : 0);
        assert.equal(app.spans[1].exceptions[0].message, 'connect ECONNREFUSED');
        assert.equal(app.outbound.length, 1);
    });

    test(`frontend destroys timed-out backend requests in ${mode} mode`, async () => {
        const app = service('frontend', enabled, undefined, { failure: 'timeout' });
        const response = await app.requestAsync('/api/checkout', 'POST');
        assert.equal(response.status, enabled ? 500 : 200);
        assert.deepEqual(response.body, { error: 'timeout' });
        assert.equal(app.spans[0].exceptions.length, enabled ? 1 : 0);
        assert.equal(app.destroyed, true);
        assert.equal(app.outbound.length, 1);
        assert.equal(app.outbound[0].timeout, 30000);
    });
}
