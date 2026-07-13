package io.clarion.logback;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.classic.spi.IThrowableProxy;
import ch.qos.logback.classic.spi.ThrowableProxyUtil;
import ch.qos.logback.core.AppenderBase;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Non-blocking Logback appender for Clarion's batch ingest endpoint.
 *
 * <p>The application thread only snapshots the event and calls {@code offer} on
 * a bounded queue. A daemon worker owns all networking. When the queue is full,
 * the oldest event is discarded so a logging outage cannot apply backpressure
 * to the host application.</p>
 */
public final class ClarionAppender extends AppenderBase<ILoggingEvent> {
    private String endpoint = "http://127.0.0.1:8080/v1/events:batch";
    private String productLine;
    private String service;
    private String environment = "prod";
    private String release;
    private int queueCapacity = 1024;
    private int batchSize = 100;
    private long flushIntervalMillis = 1000;
    private long connectTimeoutMillis = 500;
    private long requestTimeoutMillis = 1000;
    private long stopTimeoutMillis = 2000;

    private final AtomicLong dropped = new AtomicLong();
    private final AtomicLong sent = new AtomicLong();
    private final AtomicLong failedBatches = new AtomicLong();
    private ArrayBlockingQueue<EventSnapshot> queue;
    private HttpClient client;
    private URI endpointUri;
    private Thread worker;

    @Override
    public void start() {
        if (isStarted()) {
            return;
        }
        if (isBlank(productLine) || isBlank(service)) {
            addError("productLine and service are required");
            return;
        }
        if (queueCapacity < 1 || batchSize < 1 || batchSize > 500
                || flushIntervalMillis < 1 || connectTimeoutMillis < 1
                || requestTimeoutMillis < 1 || stopTimeoutMillis < 1) {
            addError("queueCapacity and timeouts must be positive; batchSize must be 1..500");
            return;
        }
        try {
            endpointUri = URI.create(endpoint);
            queue = new ArrayBlockingQueue<>(queueCapacity);
            client = HttpClient.newBuilder()
                    .connectTimeout(Duration.ofMillis(connectTimeoutMillis))
                    .build();
        } catch (RuntimeException exception) {
            addError("invalid Clarion appender configuration", exception);
            return;
        }

        super.start();
        worker = new Thread(this::runWorker, "clarion-logback-sender");
        worker.setDaemon(true);
        worker.start();
    }

    @Override
    protected void append(ILoggingEvent event) {
        if (!event.getLevel().isGreaterOrEqual(Level.ERROR)) {
            return;
        }
        EventSnapshot snapshot = EventSnapshot.from(event, productLine, service, environment, release);
        if (queue.offer(snapshot)) {
            return;
        }
        EventSnapshot discarded = queue.poll();
        if (discarded != null) {
            dropped.incrementAndGet();
        }
        if (!queue.offer(snapshot)) {
            dropped.incrementAndGet();
        }
    }

    @Override
    public void stop() {
        if (!isStarted()) {
            return;
        }
        super.stop();
        try {
            worker.join(stopTimeoutMillis);
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
        }
        if (worker.isAlive()) {
            dropped.addAndGet(queue.size());
            queue.clear();
            worker.interrupt();
        }
    }

    private void runWorker() {
        List<EventSnapshot> batch = new ArrayList<>(batchSize);
        while (isStarted() || !queue.isEmpty()) {
            try {
                EventSnapshot first = queue.poll(flushIntervalMillis, TimeUnit.MILLISECONDS);
                if (first == null) {
                    continue;
                }
                batch.add(first);
                queue.drainTo(batch, batchSize - 1);
                send(batch);
            } catch (InterruptedException exception) {
                Thread.currentThread().interrupt();
                dropped.addAndGet(batch.size() + queue.size());
                queue.clear();
                return;
            } catch (RuntimeException exception) {
                failedBatches.incrementAndGet();
                dropped.addAndGet(batch.size());
                addWarn("Clarion batch send failed", exception);
            } finally {
                batch.clear();
            }
        }
    }

    private void send(List<EventSnapshot> batch) {
        HttpRequest request = HttpRequest.newBuilder(endpointUri)
                .timeout(Duration.ofMillis(requestTimeoutMillis))
                .header("content-type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(toJson(batch)))
                .build();
        try {
            HttpResponse<Void> response = client.send(request, HttpResponse.BodyHandlers.discarding());
            if (response.statusCode() >= 200 && response.statusCode() < 300) {
                sent.addAndGet(batch.size());
            } else {
                failedBatches.incrementAndGet();
                dropped.addAndGet(batch.size());
                addWarn("Clarion rejected batch with HTTP " + response.statusCode());
            }
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
            failedBatches.incrementAndGet();
            dropped.addAndGet(batch.size());
        } catch (Exception exception) {
            failedBatches.incrementAndGet();
            dropped.addAndGet(batch.size());
            addWarn("Clarion batch send failed: " + exception.getClass().getSimpleName());
        }
    }

    private static String toJson(List<EventSnapshot> events) {
        StringBuilder json = new StringBuilder(128 + events.size() * 256).append("{\"events\":[");
        for (int i = 0; i < events.size(); i++) {
            if (i > 0) {
                json.append(',');
            }
            events.get(i).appendJson(json);
        }
        return json.append("]}").toString();
    }

    private static boolean isBlank(String value) {
        return value == null || value.trim().isEmpty();
    }

    public long getDroppedCount() { return dropped.get(); }
    public long getSentCount() { return sent.get(); }
    public long getFailedBatchCount() { return failedBatches.get(); }
    public int getQueuedCount() { return queue == null ? 0 : queue.size(); }

    public void setEndpoint(String endpoint) { this.endpoint = endpoint; }
    public void setProductLine(String productLine) { this.productLine = productLine; }
    public void setService(String service) { this.service = service; }
    public void setEnvironment(String environment) { this.environment = environment; }
    public void setRelease(String release) { this.release = release; }
    public void setQueueCapacity(int queueCapacity) { this.queueCapacity = queueCapacity; }
    public void setBatchSize(int batchSize) { this.batchSize = batchSize; }
    public void setFlushIntervalMillis(long value) { this.flushIntervalMillis = value; }
    public void setConnectTimeoutMillis(long value) { this.connectTimeoutMillis = value; }
    public void setRequestTimeoutMillis(long value) { this.requestTimeoutMillis = value; }
    public void setStopTimeoutMillis(long value) { this.stopTimeoutMillis = value; }

    private static final class EventSnapshot {
        private final String productLine;
        private final String service;
        private final String environment;
        private final String release;
        private final String logger;
        private final String exceptionType;
        private final String message;
        private final String stacktrace;
        private final Instant occurredAt;

        private EventSnapshot(String productLine, String service, String environment, String release,
                              String logger, String exceptionType, String message, String stacktrace,
                              Instant occurredAt) {
            this.productLine = productLine;
            this.service = service;
            this.environment = environment;
            this.release = release;
            this.logger = logger;
            this.exceptionType = exceptionType;
            this.message = message;
            this.stacktrace = stacktrace;
            this.occurredAt = occurredAt;
        }

        static EventSnapshot from(ILoggingEvent event, String productLine, String service,
                                  String environment, String release) {
            IThrowableProxy throwable = event.getThrowableProxy();
            String exceptionType = throwable == null ? "logback." + event.getLevel() : throwable.getClassName();
            String stacktrace = throwable == null ? "" : ThrowableProxyUtil.asString(throwable);
            return new EventSnapshot(productLine, service, environment, release,
                    event.getLoggerName(), exceptionType, event.getFormattedMessage(), stacktrace,
                    Instant.ofEpochMilli(event.getTimeStamp()));
        }

        void appendJson(StringBuilder json) {
            json.append('{');
            field(json, "product_line", productLine, false);
            field(json, "service", service, true);
            field(json, "environment", environment, true);
            if (!isBlank(release)) {
                field(json, "release", release, true);
            }
            field(json, "logger", logger, true);
            field(json, "exception_type", exceptionType, true);
            field(json, "message", message, true);
            field(json, "stacktrace", stacktrace, true);
            field(json, "occurred_at", occurredAt.toString(), true);
            json.append('}');
        }

        private static void field(StringBuilder json, String name, String value, boolean comma) {
            if (comma) {
                json.append(',');
            }
            quote(json, name).append(':');
            quote(json, value == null ? "" : value);
        }

        private static StringBuilder quote(StringBuilder json, String value) {
            json.append('"');
            for (int i = 0; i < value.length(); i++) {
                char character = value.charAt(i);
                switch (character) {
                    case '"': json.append("\\\""); break;
                    case '\\': json.append("\\\\"); break;
                    case '\b': json.append("\\b"); break;
                    case '\f': json.append("\\f"); break;
                    case '\n': json.append("\\n"); break;
                    case '\r': json.append("\\r"); break;
                    case '\t': json.append("\\t"); break;
                    default:
                        if (character < 0x20) {
                            json.append(String.format("\\u%04x", (int) character));
                        } else {
                            json.append(character);
                        }
                }
            }
            return json.append('"');
        }
    }
}
