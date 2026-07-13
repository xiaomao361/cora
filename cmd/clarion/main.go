package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/claracore/clarion/internal/clarion"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "clarion.db", "SQLite database path")
	flushInterval := flag.Duration("flush-interval", 10*time.Second, "aggregation flush interval")
	maxActive := flag.Int("max-active", 10000, "maximum active fingerprints per window")
	flag.Parse()

	store, err := clarion.OpenStore(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	aggregator := clarion.NewAggregator(store, *maxActive)
	go aggregator.Run(ctx, *flushInterval)
	server := &http.Server{Addr: *addr, Handler: clarion.Handler(store, aggregator)}
	go func() {
		log.Printf("Clarion listening on %s", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	if err := aggregator.Flush(shutdownCtx); err != nil {
		log.Printf("final flush: %v", err)
	}
}
