package example;

import ch.qos.logback.classic.LoggerContext;
import io.clarion.logback.ClarionAppender;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public final class ExampleApp {
    private static final Logger LOG = LoggerFactory.getLogger(ExampleApp.class);

    private ExampleApp() {}

    public static void main(String[] args) {
        LOG.info("This event stays local");
        LOG.error("Example order {} failed", 42, new IllegalStateException("example failure"));
        LoggerContext context = (LoggerContext) LoggerFactory.getILoggerFactory();
        ClarionAppender appender = (ClarionAppender) context.getLogger(Logger.ROOT_LOGGER_NAME)
                .getAppender("CLARION");
        appender.stop();
        System.out.printf("Clarion sent=%d dropped=%d failed_batches=%d%n",
                appender.getSentCount(), appender.getDroppedCount(), appender.getFailedBatchCount());
        context.stop();
    }
}
