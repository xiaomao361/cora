# Cora Performance Baseline

Measured on 2026-07-13 on an Apple M4 (`darwin/arm64`) with the Go version in
`go.mod`. These numbers are a development baseline, not a production capacity
claim.

## Reproduce

```sh
go test ./internal/cora -run '^$' \
  -bench 'BenchmarkAggregator(Add|Flush)$' \
  -benchmem -benchtime=1s -count=3
go test -v ./internal/cora \
  -run 'Test(DefaultAggregatorCapacity|FlushSQLWork)'
```

## Results

| Scenario | Time | Approximate throughput | Allocations |
| --- | ---: | ---: | ---: |
| Repeated fingerprint ingest | 887–912 ns/event | 1.10–1.13M events/s | 480 B, 9 allocs/event |
| 10,000 active fingerprints ingest | 950–955 ns/event | 1.05M events/s | 515–516 B, 9 allocs/event |
| Flush 1 fingerprint | 53–57 µs | — | 5.9 KB |
| Flush 100 fingerprints | 2.45–2.54 ms | — | 540 KB |
| Flush 10,000 fingerprints | 255–280 ms | — | 52.5 MB |

The bounded-memory test retained 11.92 MiB of Go heap for 10,000 representative
events with realistic message and stack strings. A real HTTP process sampled at
18.0 MiB RSS before ingest, 36.4 MiB with 10,000 pending fingerprints, and
47.1 MiB after flush. These RSS values are snapshots rather than a continuously
sampled peak.

A deterministic test sends 10,000 events spread over 100 fingerprints and
asserts that one flush produces exactly 100 Problem rows and 100 trend rows.
SQLite work therefore scales with distinct fingerprints in a window, not raw
event count.

## Decision

Keep the current defaults of a 10-second window and 10,000 active fingerprints.
The measured retained heap and observed process RSS are comfortably below the
100 MiB target on this machine, and a full 10,000-fingerprint flush completes in
under 300 ms. Revisit the defaults after Cora Agent runs on the intended Linux
hosts with sustained production event shapes.

The main performance concern is allocation churn during a maximum-cardinality
flush. It is acceptable for the current experimental slice, but should be
profiled before increasing `max-active` or shortening the flush interval.
