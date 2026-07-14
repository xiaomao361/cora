package cora

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func BenchmarkAggregatorAdd(b *testing.B) {
	repeated := benchmarkEvent(0)
	highCardinality := make([]Event, 10_000)
	for i := range highCardinality {
		highCardinality[i] = benchmarkEvent(i)
	}

	b.Run("repeated", func(b *testing.B) {
		aggregator := NewAggregator(nil, 10_000)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := aggregator.Add(repeated); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("10000-active-fingerprints", func(b *testing.B) {
		aggregator := NewAggregator(nil, 10_000)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := aggregator.Add(highCardinality[i%len(highCardinality)]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkAggregatorFlush(b *testing.B) {
	for _, cardinality := range []int{1, 100, 10_000} {
		b.Run(fmt.Sprintf("%d-fingerprints", cardinality), func(b *testing.B) {
			store, err := OpenStore(b.TempDir() + "/cora.db")
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				aggregator := NewAggregator(store, cardinality)
				for fingerprint := 0; fingerprint < cardinality; fingerprint++ {
					if err := aggregator.Add(benchmarkEvent(fingerprint)); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				if err := aggregator.Flush(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDefaultAggregatorCapacityStaysUnderMemoryTarget(t *testing.T) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	aggregator := NewAggregator(nil, 10_000)
	events := make([]Event, 10_000)
	for i := range events {
		events[i] = benchmarkEvent(i)
		if err := aggregator.Add(events[i]); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(events)
	runtime.KeepAlive(aggregator)

	used := after.HeapAlloc - before.HeapAlloc
	t.Logf("10,000 active fingerprints retained %.2f MiB of Go heap", float64(used)/(1024*1024))
	const target = 100 * 1024 * 1024
	if used >= target {
		t.Fatalf("retained heap=%d bytes, target is below %d bytes", used, target)
	}
}

func TestFlushSQLWorkScalesWithDistinctFingerprints(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 100)
	for fingerprint := 0; fingerprint < 100; fingerprint++ {
		for repeat := 0; repeat < 100; repeat++ {
			if err := aggregator.Add(benchmarkEvent(fingerprint)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var problems, trendPoints int
	if err := store.db.QueryRow(`SELECT count(*) FROM problems`).Scan(&problems); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM trend_points`).Scan(&trendPoints); err != nil {
		t.Fatal(err)
	}
	if problems != 100 || trendPoints != 100 {
		t.Fatalf("10,000 events across 100 fingerprints wrote %d problems and %d trend points", problems, trendPoints)
	}
}

func benchmarkEvent(index int) Event {
	return Event{
		ProductLine:   "benchmark",
		Service:       "orders",
		Environment:   "prod",
		Logger:        fmt.Sprintf("com.example.Order%d", index),
		ExceptionType: "java.lang.IllegalStateException",
		Message:       strings.Repeat("representative error context ", 16),
		Stacktrace:    fmt.Sprintf("at com.example.Order%d.process(Order.java:42)\nat org.springframework.Dispatcher.run(Dispatcher.java:1)", index),
	}
}
