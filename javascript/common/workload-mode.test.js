const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const { isEvalMode, paymentDetails, fallbackUserId, shouldFail, fallbackAds } = require('./workload-mode');

const noRandom = () => { throw new Error('Unexpected random choice'); };

test('the workload requires an exact opt-in', () => {
    assert.equal(isEvalMode({ EVAL_MODE: '1' }), true);
    for (const value of [undefined, '', '0', 'true', 'yes', ' 1', '1 ']) {
        assert.equal(isEvalMode({ EVAL_MODE: value }), false);
    }
});

test('payment uses stable natural values without random choices', () => {
    assert.deepEqual(paymentDetails(true, noRandom), {
        cardNumber: '4111111111111111', loyaltyLevel: 'silver', amount: '110.00', currency: 'USD',
    });
});

test('normal payment retains the existing random ranges and ordering', () => {
    const values = [0.1, 0.6, 0.5, 0.9];
    assert.deepEqual(paymentDetails(false, () => values.shift()), {
        cardNumber: '4100000000000000', loyaltyLevel: 'gold', amount: '260.00', currency: 'JPY',
    });
    assert.equal(values.length, 0);
});

test('email fallback stays stable only when enabled', () => {
    assert.equal(fallbackUserId(true, noRandom), 'user-1042');
    assert.equal(fallbackUserId(false, () => 0.5432), 'user-5432');
});

test('failure simulation is disabled only when enabled', () => {
    for (const probability of [0.02, 0.05]) {
        assert.equal(shouldFail(probability, true, noRandom), false);
        assert.equal(shouldFail(probability, false, () => probability - 0.001), true);
        assert.equal(shouldFail(probability, false, () => probability), false);
    }
});

test('fallback ads select the first entry without mutating the catalog', () => {
    const ads = Object.freeze([{ id: 'ad-1' }, { id: 'ad-3' }, { id: 'ad-4' }]);
    assert.deepEqual(fallbackAds(ads, true, noRandom), [{ id: 'ad-1' }]);
    assert.deepEqual(fallbackAds([], true, noRandom), []);
    assert.equal(ads.length, 3);
});

test('normal fallback ads retain random count and shuffling', () => {
    let calls = 0;
    const ads = [{ id: 'first' }, { id: 'second' }, { id: 'third' }];
    const chosen = fallbackAds(ads, false, () => { calls += 1; return 0.5; });
    assert.deepEqual(chosen, ads.slice(0, 2));
    assert.ok(calls > 1);
});

function telemetryConfiguration(enabled) {
    const captured = { metricReaders: 0, metricExporters: 0, hostMetrics: 0 };
    const meter = {
        createObservableGauge() { captured.hostMetrics += 1; return { addCallback() {} }; },
        createObservableCounter() { captured.hostMetrics += 1; return { addCallback() {} }; },
    };
    class Resource { constructor(attributes) { this.attributes = attributes; } }
    class NodeSDK {
        constructor(options) { captured.options = options; }
        start() {}
    }
    class LoggerProvider {
        constructor(options) { captured.logResource = options.resource; }
        addLogRecordProcessor() {}
    }
    class MetricReader { constructor() { captured.metricReaders += 1; } }
    class MetricExporter { constructor() { captured.metricExporters += 1; } }
    class Stub {}
    const imports = {
        './workload-mode': { isEvalMode: () => enabled },
        '@opentelemetry/sdk-node': { NodeSDK },
        '@opentelemetry/resources': { Resource },
        '@opentelemetry/instrumentation-http': { HttpInstrumentation: Stub },
        '@opentelemetry/api': {
            trace: { getTracer() {} }, metrics: { getMeter: () => meter },
            propagation: { setGlobalPropagator() {} },
        },
        '@opentelemetry/api-logs': { logs: { setGlobalLoggerProvider() {}, getLogger() {} } },
        '@opentelemetry/core': {
            W3CTraceContextPropagator: Stub, CompositePropagator: Stub, W3CBaggagePropagator: Stub,
        },
        '@opentelemetry/exporter-trace-otlp-http': { OTLPTraceExporter: Stub },
        '@opentelemetry/exporter-metrics-otlp-http': { OTLPMetricExporter: MetricExporter },
        '@opentelemetry/exporter-logs-otlp-http': { OTLPLogExporter: Stub },
        '@opentelemetry/sdk-metrics': { PeriodicExportingMetricReader: MetricReader },
        '@opentelemetry/sdk-logs': { LoggerProvider, SimpleLogRecordProcessor: Stub },
        os: require('node:os'),
    };
    const sandbox = {
        module: { exports: {} }, console: { log() {} },
        process: { env: {}, cpuUsage: () => ({ user: 0, system: 0 }) },
        require(name) {
            assert.ok(Object.hasOwn(imports, name), `Unexpected import: ${name}`);
            return imports[name];
        },
    };
    vm.runInNewContext(readFileSync(path.join(__dirname, 'telemetry.js'), 'utf8'), sandbox);
    sandbox.module.exports.initTelemetry('payment');
    return captured;
}

test('telemetry retains request providers and natural resources without periodic or detected resources', () => {
    const captured = telemetryConfiguration(true);
    assert.equal(captured.options.autoDetectResources, false);
    assert.equal(captured.options.metricReader, undefined);
    assert.equal(captured.metricReaders, 0);
    assert.equal(captured.metricExporters, 0);
    assert.equal(captured.hostMetrics, 0);
    assert.ok(captured.options.traceExporter);
    assert.equal(captured.logResource, captured.options.resource);
    assert.equal(captured.options.resource.attributes.account, 'northstar-retail');
    assert.equal(captured.options.resource.attributes['deployment.environment'], 'prod');
    assert.equal(captured.options.resource.attributes.cluster, 'main');
    assert.equal(captured.options.resource.attributes['service.name'], 'payment');
    assert.ok(Object.keys(captured.options.resource.attributes).every(key => !/eval/i.test(key)));
});

test('normal telemetry retains metric readers, host metrics, and SDK detection defaults', () => {
    const captured = telemetryConfiguration(false);
    assert.equal(captured.options.autoDetectResources, undefined);
    assert.ok(captured.options.metricReader);
    assert.equal(captured.metricReaders, 1);
    assert.equal(captured.metricExporters, 1);
    assert.equal(captured.hostMetrics, 4);
    assert.equal(captured.options.resource.attributes.account, undefined);
});
