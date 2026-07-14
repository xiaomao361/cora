package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/claracore/cora/internal/auth"
	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/cora"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "explicit HTTP listen address")
	dbPath := flag.String("db", "cora.db", "SQLite database path")
	authTokenFile := flag.String("auth-token-file", "", "file containing the required bearer token")
	allowUnauthenticated := flag.Bool("allow-unauthenticated", false, "allow local development without authentication")
	flushInterval := flag.Duration("flush-interval", 10*time.Second, "aggregation flush interval")
	maxActive := flag.Int("max-active", 10000, "maximum active fingerprints per window")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	checkDB := flag.Bool("check-db", false, "run SQLite quick_check and exit")
	backupDB := flag.String("backup-db", "", "write a verified SQLite backup to this new path and exit")
	flag.Parse()
	if *showVersion {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}
	if *checkDB || *backupDB != "" {
		store, err := cora.OpenStore(*dbPath)
		if err != nil {
			log.Fatal(err)
		}
		defer store.Close()
		if err := store.IntegrityCheck(context.Background()); err != nil {
			log.Fatal(err)
		}
		if *backupDB != "" {
			if err := store.Backup(context.Background(), *backupDB); err != nil {
				log.Fatal(err)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"status": "ok", "database": *dbPath, "backup": *backupDB,
			"storage": store.Health(), "build": buildinfo.Current(),
		})
		return
	}
	if *authTokenFile == "" && !*allowUnauthenticated {
		log.Fatal("auth-token-file is required; use allow-unauthenticated only for local development")
	}
	bearerToken := ""
	if *authTokenFile != "" {
		var err error
		bearerToken, err = auth.LoadBearerTokenFile(*authTokenFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	store, err := cora.OpenStore(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	aggregator := cora.NewAggregator(store, *maxActive)
	go aggregator.Run(ctx, *flushInterval)
	server := &http.Server{
		Addr: *addr, Handler: cora.HandlerWithOptions(store,
			cora.HandlerOptions{BearerToken: bearerToken, MCPHandler: cora.NewMCPHandler(store), BuildInfo: buildinfo.Current()}, aggregator),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("Cora listening on %s", *addr)
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
