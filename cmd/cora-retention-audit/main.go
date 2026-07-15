package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/retention"
)

func main() {
	database := flag.String("db", "", "consistent SQLite backup; opened with mode=ro")
	productLine := flag.String("product-line", "", "required product-line boundary")
	coraBuildVersion := flag.String("cora-build-version", "", "deployed Cora build version associated with the backup")
	coraSourceDigest := flag.String("cora-source-digest", "", "full deployed Cora source SHA-256 associated with the backup")
	iterationRoot := flag.String("iteration-root", "out/iterations", "immutable iteration artifact root")
	closureRoot := flag.String("closure-root", "out/closure-receipts", "closure receipt and observation artifact root")
	outputRoot := flag.String("output-root", "out/retention-audits", "immutable retention audit root")
	runID := flag.String("run-id", "", "required stable audit identifier")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()
	if *showVersion {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}
	if *database == "" || *productLine == "" || *coraBuildVersion == "" || *coraSourceDigest == "" || *runID == "" {
		fatal(fmt.Errorf("db, product-line, cora-build-version, cora-source-digest, and run-id are required"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := retention.RunAudit(ctx, retention.Config{
		DatabasePath: filepath.Clean(*database), ProductLine: *productLine,
		CoraBuildVersion: *coraBuildVersion, CoraSourceDigest: *coraSourceDigest,
		IterationRoot: filepath.Clean(*iterationRoot), ClosureRoot: filepath.Clean(*closureRoot),
		OutputRoot: filepath.Clean(*outputRoot), RunID: *runID,
	})
	if err != nil {
		fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
