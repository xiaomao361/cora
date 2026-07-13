# Clarion Logback Appender

`clarion-logback` sends ERROR events to Clarion without doing network I/O on
the application thread. It requires Java 11 or newer and works with Logback
1.5.x.

## Build

```sh
cd integrations/logback
mvn test
mvn install
```

Add `io.clarion:clarion-logback:0.1.0-SNAPSHOT` to the application together
with its existing `logback-classic` dependency, then configure `logback.xml`:

```xml
<appender name="CLARION" class="io.clarion.logback.ClarionAppender">
  <endpoint>http://127.0.0.1:8080/v1/events:batch</endpoint>
  <productLine>payments</productLine>
  <service>checkout</service>
  <environment>prod</environment>
  <release>2026.07.13</release>
  <queueCapacity>1024</queueCapacity>
  <batchSize>100</batchSize>
  <flushIntervalMillis>1000</flushIntervalMillis>
  <connectTimeoutMillis>500</connectTimeoutMillis>
  <requestTimeoutMillis>1000</requestTimeoutMillis>
  <stopTimeoutMillis>2000</stopTimeoutMillis>
</appender>

<root level="INFO">
  <appender-ref ref="CLARION"/>
</root>
```

Only ERROR and higher events are captured. The application thread snapshots
the event and uses a non-blocking bounded queue. When full, the queue discards
its oldest event and retains the latest one. HTTP failures and rejected batches
are not retried; they are counted as dropped so a Clarion outage cannot build
unbounded memory or delay the host application.

The counters `getSentCount()`, `getDroppedCount()`, `getFailedBatchCount()`, and
`getQueuedCount()` are available for JMX/metrics integration by the host. That
integration is intentionally not included in this first slice.

## End-to-end example

Start Clarion from the repository root, build and install the appender, then run:

```sh
cd integrations/logback
mvn install
CLARION_ENDPOINT=http://127.0.0.1:8080/v1/events:batch \
  mvn -f example/pom.xml compile exec:java
```

The example emits one local INFO and one ERROR with an exception. It stops the
Appender explicitly so its final batch is drained before the short-lived JVM
exits, and prints the resulting sent/dropped counters.
