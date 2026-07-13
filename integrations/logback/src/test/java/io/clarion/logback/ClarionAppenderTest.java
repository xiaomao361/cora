package io.clarion.logback;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.LoggerContext;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ClarionAppenderTest {
    private HttpServer server;
    private ClarionAppender appender;

    @AfterEach
    void cleanup() {
        if (appender != null) {
            appender.stop();
        }
        if (server != null) {
            server.stop(0);
        }
    }

    @Test
    void asynchronouslyPostsExceptionAsClarionEvent() throws Exception {
        List<String> requests = new CopyOnWriteArrayList<>();
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/v1/events:batch", exchange -> {
            requests.add(new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8));
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
        });
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();

        appender = configuredAppender("http://127.0.0.1:" + server.getAddress().getPort() + "/v1/events:batch");
        appender.start();
        assertTrue(appender.isStarted());

        logger().error("order {} failed", 42, new IllegalStateException("boom"));

        await(() -> appender.getSentCount() == 1, 2000);
        assertEquals(1, requests.size());
        String body = requests.get(0);
        assertTrue(body.contains("\"product_line\":\"demo\""));
        assertTrue(body.contains("\"exception_type\":\"java.lang.IllegalStateException\""));
        assertTrue(body.contains("order 42 failed"));
        assertTrue(body.contains("ClarionAppenderTest"));
        assertEquals(0, appender.getDroppedCount());
    }

    @Test
    void unavailableClarionNeverBlocksLoggerAndCountsDroppedEvents() throws Exception {
        appender = configuredAppender("http://127.0.0.1:1/v1/events:batch");
        appender.setQueueCapacity(4);
        appender.setBatchSize(2);
        appender.setConnectTimeoutMillis(50);
        appender.setRequestTimeoutMillis(80);
        appender.start();

        Logger logger = logger();
        long started = System.nanoTime();
        for (int i = 0; i < 1000; i++) {
            logger.error("failure {}", i);
        }
        long elapsedMillis = TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - started);

        assertTrue(elapsedMillis < 1000, "logging took " + elapsedMillis + "ms");
        await(() -> appender.getDroppedCount() == 1000, 3000);
        assertTrue(appender.getFailedBatchCount() > 0);
        assertEquals(0, appender.getSentCount());
    }

    @Test
    void refusesToStartWithoutRequiredIdentity() {
        appender = new ClarionAppender();
        appender.setService("orders");
        appender.start();
        assertFalse(appender.isStarted());
    }

    private ClarionAppender configuredAppender(String endpoint) {
        ClarionAppender result = new ClarionAppender();
        result.setContext((LoggerContext) LoggerFactory.getILoggerFactory());
        result.setEndpoint(endpoint);
        result.setProductLine("demo");
        result.setService("orders");
        result.setEnvironment("test");
        result.setBatchSize(10);
        result.setFlushIntervalMillis(20);
        return result;
    }

    private Logger logger() {
        Logger logger = ((LoggerContext) LoggerFactory.getILoggerFactory()).getLogger("example.orders");
        logger.detachAndStopAllAppenders();
        logger.setAdditive(false);
        logger.setLevel(Level.ERROR);
        logger.addAppender(appender);
        return logger;
    }

    private static void await(Check check, long timeoutMillis) throws Exception {
        long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMillis);
        while (!check.done() && System.nanoTime() < deadline) {
            Thread.sleep(10);
        }
        assertTrue(check.done(), "condition not reached before timeout");
    }

    private interface Check { boolean done() throws IOException; }
}
