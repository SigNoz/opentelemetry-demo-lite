/**
 * Email Service - Order confirmation emails
 */
const http = require('http');
const url = require('url');
const { randomUUID } = require('crypto');
const { isEvalMode, fallbackUserId, shouldFail } = require('./common/workload-mode');
const { initTelemetry, registerShutdownHandler, emitLog, trace, propagation, context, SpanKind } = require('./common/telemetry');

const PORT = process.env.PORT || 8088;
const { tracer, meter, logger } = initTelemetry('email');

const emailsCounter = meter.createCounter('app.email.sent', { unit: '{emails}' });
const emailLatency = meter.createHistogram('app.email.latency', { unit: 'ms' });

const server = http.createServer((req, res) => {
    const parsedUrl = url.parse(req.url, true);
    const ctx = propagation.extract(context.active(), req.headers);

    if (isEvalMode() && parsedUrl.pathname === '/health' && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end('{"status":"ok"}');
    } else if (parsedUrl.pathname === '/send' && req.method === 'POST') {
        handleSendEmail(req, res, ctx, parsedUrl.query);
    } else {
        res.writeHead(404);
        res.end('{"error":"Not found"}');
    }
});

function handleSendEmail(req, res, parentCtx, query) {
    const start = Date.now();

    const span = tracer.startSpan('sendOrderConfirmation', {
        kind: SpanKind.SERVER,
        attributes: { 'rpc.system': 'grpc', 'rpc.service': 'oteldemo.EmailService', 'rpc.method': 'SendOrderConfirmation' },
    }, parentCtx);

    context.with(trace.setSpan(parentCtx, span), () => {
        try {
            const orderId = query.order_id || randomUUID();
            const userId = query.user_id || fallbackUserId();
            const email = query.email || `${userId}@example.com`;

            span.setAttributes({
                'app.order.id': orderId,
                'app.user.id': userId,
                'app.email.recipient': email,
                'app.email.type': 'order_confirmation',
            });

            // Read baggage
            const baggage = propagation.getBaggage(trace.setSpan(parentCtx, span));
            if (baggage) {
                const sessionId = baggage.getEntry('session.id');
                if (sessionId) span.setAttribute('session.id', sessionId.value);
            }

            if (shouldFail(0.02)) {
                const error = new Error('SMTP connection failed');
                span.recordException(error);
                span.setStatus({ code: 2, message: error.message });
                span.addEvent('email_failed', { 'app.email.failure_reason': 'smtp_error' });
                emailsCounter.add(1, { type: 'order_confirmation', status: 'failed' });
                res.writeHead(500);
                res.end(JSON.stringify({ error: error.message }));
            } else {
                const messageId = `msg-${randomUUID().slice(0, 8)}`;
                span.setAttribute('app.email.message_id', messageId);
                span.addEvent('email_sent', { 'app.email.recipient': email, 'app.email.message_id': messageId });
                emailsCounter.add(1, { type: 'order_confirmation', status: 'sent' });
                emitLog(logger, `Email sent to ${email} for order ${orderId}`, { 'email.recipient': email, 'order.id': orderId });
                res.writeHead(200, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify({ success: true, message_id: messageId, recipient: email, order_id: orderId }));
            }

            emailLatency.record(Date.now() - start);
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

server.listen(PORT, () => console.log(`Email Service listening on port ${PORT}`));
registerShutdownHandler();
