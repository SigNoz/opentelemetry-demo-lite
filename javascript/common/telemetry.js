const { NodeSDK } = require('@opentelemetry/sdk-node');
const { Resource } = require('@opentelemetry/resources');
const { HttpInstrumentation } = require('@opentelemetry/instrumentation-http');
const { trace, metrics, propagation, context, SpanKind, SpanStatusCode } = require('@opentelemetry/api');
const { logs, SeverityNumber } = require('@opentelemetry/api-logs');
const { W3CTraceContextPropagator, CompositePropagator, W3CBaggagePropagator } = require('@opentelemetry/core');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-http');
const { OTLPMetricExporter } = require('@opentelemetry/exporter-metrics-otlp-http');
const { OTLPLogExporter } = require('@opentelemetry/exporter-logs-otlp-http');
const { PeriodicExportingMetricReader, MeterProvider } = require('@opentelemetry/sdk-metrics');
const { LoggerProvider, SimpleLogRecordProcessor } = require('@opentelemetry/sdk-logs');
const os = require('os');

let sdk = null;
let loggerProvider = null;

function initTelemetry(defaultServiceName) {
    const serviceName = process.env.OTEL_SERVICE_NAME || defaultServiceName;
    const otlpEndpoint = process.env.OTEL_EXPORTER_OTLP_ENDPOINT_HTTP ||
        process.env.OTEL_EXPORTER_OTLP_ENDPOINT?.replace(':4317', ':4318') ||
        'http://localhost:4318';

    const resource = new Resource({
        'service.name': serviceName,
        'service.version': '1.0.0',
        'telemetry.sdk.language': 'javascript',
        'host.name': `${serviceName}-host`,
        'os.type': os.platform(),
    });

    const traceExporter = new OTLPTraceExporter({ url: `${otlpEndpoint}/v1/traces` });
    const metricReader = new PeriodicExportingMetricReader({
        exporter: new OTLPMetricExporter({ url: `${otlpEndpoint}/v1/metrics` }),
        exportIntervalMillis: 5000,
    });

    // Initialize Logs SDK
    const logExporter = new OTLPLogExporter({ url: `${otlpEndpoint}/v1/logs` });
    loggerProvider = new LoggerProvider({ resource });
    loggerProvider.addLogRecordProcessor(new SimpleLogRecordProcessor(logExporter));
    logs.setGlobalLoggerProvider(loggerProvider);

    sdk = new NodeSDK({
        resource,
        traceExporter,
        metricReader,
        instrumentations: [new HttpInstrumentation()],
    });

    propagation.setGlobalPropagator(
        new CompositePropagator({
            propagators: [new W3CTraceContextPropagator(), new W3CBaggagePropagator()],
        })
    );

    sdk.start();
    console.log(`[OTel] ${serviceName} initialized → ${otlpEndpoint}`);

    // Start custom host metrics that emit exactly what SigNoz Infrastructure expects
    // Note: We don't use @opentelemetry/host-metrics as it emits system.cpu.utilization
    // but SigNoz expects system.cpu.time with state attribute
    startHostMetrics(serviceName);
    console.log(`[OTel] ${serviceName} host metrics started`);

    return {
        sdk,
        provider: sdk,
        tracer: trace.getTracer(serviceName),
        meter: metrics.getMeter(serviceName),
        logger: logs.getLogger(serviceName),
    };
}

// Start custom host metrics that emit exactly what SigNoz expects
// SigNoz requires:
// - system.cpu.load_average.15m for ACTIVE status
// - system.cpu.time with state attribute for CPU %
// - system.memory.usage with state attribute for Memory %
function startHostMetrics(serviceName) {
    const meter = metrics.getMeter('host-metrics');

    // Track cumulative CPU time (in seconds)
    let lastCpuUsage = process.cpuUsage();
    let cumulativeCpuUser = 0;
    let cumulativeCpuSystem = 0;
    let cumulativeCpuIdle = 0;
    const startTime = Date.now();

    // system.cpu.load_average.* - REQUIRED for ACTIVE status in SigNoz
    const loadAvg1m = meter.createObservableGauge('system.cpu.load_average.1m', {
        description: '1-minute CPU load average',
        unit: '1',
    });

    const loadAvg5m = meter.createObservableGauge('system.cpu.load_average.5m', {
        description: '5-minute CPU load average',
        unit: '1',
    });

    const loadAvg15m = meter.createObservableGauge('system.cpu.load_average.15m', {
        description: '15-minute CPU load average',
        unit: '1',
    });

    // system.cpu.time - cumulative CPU time with state attribute
    // SigNoz calculates CPU % as: rate(cpu_time{state!=idle}) / rate(cpu_time{total})
    const cpuTime = meter.createObservableCounter('system.cpu.time', {
        description: 'Seconds each CPU state',
        unit: 's',
    });

    // system.memory.usage - memory usage with state attribute
    // SigNoz calculates Memory % as: usage{state=used} / usage{total}
    // Using Gauge because SigNoz graph query auto-detection defaults to Unspecified temporality
    const memoryUsage = meter.createObservableGauge('system.memory.usage', {
        description: 'Memory usage by state',
        unit: 'By',
    });

    // Register callbacks
    loadAvg1m.addCallback((observableResult) => {
        observableResult.observe(os.loadavg()[0]);
    });

    loadAvg5m.addCallback((observableResult) => {
        observableResult.observe(os.loadavg()[1]);
    });

    loadAvg15m.addCallback((observableResult) => {
        observableResult.observe(os.loadavg()[2]);
    });

    cpuTime.addCallback((observableResult) => {
        // Get CPU usage since last call (in microseconds)
        const currentUsage = process.cpuUsage(lastCpuUsage);
        lastCpuUsage = process.cpuUsage();

        // Convert to seconds and accumulate
        cumulativeCpuUser += currentUsage.user / 1e6;
        cumulativeCpuSystem += currentUsage.system / 1e6;

        // Calculate idle time based on elapsed wall clock time
        const elapsedSeconds = (Date.now() - startTime) / 1000;
        const numCpus = os.cpus().length;
        const totalCpuTime = elapsedSeconds * numCpus;
        cumulativeCpuIdle = Math.max(0, totalCpuTime - cumulativeCpuUser - cumulativeCpuSystem);

        // Emit CPU time for each state
        observableResult.observe(cumulativeCpuUser, { state: 'user' });
        observableResult.observe(cumulativeCpuSystem, { state: 'system' });
        observableResult.observe(cumulativeCpuIdle, { state: 'idle' });
    });

    memoryUsage.addCallback((observableResult) => {
        const totalMem = os.totalmem();
        const freeMem = os.freemem();
        const usedMem = totalMem - freeMem;

        // Emit memory usage for each state
        observableResult.observe(usedMem, { state: 'used' });
        observableResult.observe(freeMem, { state: 'free' });
    });
}

function emitLog(logger, message, attributes = {}, severity = 'INFO') {
    const activeContext = context.active();
    const span = trace.getSpan(activeContext);
    const spanContext = span?.spanContext();

    const severityMap = {
        'DEBUG': SeverityNumber.DEBUG,
        'INFO': SeverityNumber.INFO,
        'WARN': SeverityNumber.WARN,
        'ERROR': SeverityNumber.ERROR,
    };

    logger.emit({
        severityNumber: severityMap[severity] || SeverityNumber.INFO,
        severityText: severity,
        body: message,
        attributes: {
            ...attributes,
        },
        context: activeContext,
    });
}

function shutdown() {
    const promises = [];
    if (sdk) {
        promises.push(sdk.shutdown());
    }
    if (loggerProvider) {
        promises.push(loggerProvider.shutdown());
    }
    return Promise.all(promises);
}

module.exports = {
    initTelemetry,
    shutdown,
    emitLog,
    trace,
    metrics,
    logs,
    propagation,
    context,
    SpanKind,
    SpanStatusCode,
    SeverityNumber
};
