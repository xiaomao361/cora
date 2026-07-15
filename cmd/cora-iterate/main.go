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
	"time"

	"github.com/claracore/cora/internal/auth"
	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/iteration"
)

func main() {
	serverURL := flag.String("server-url", "", "Cora Server base URL")
	tokenFile := flag.String("auth-token-file", "", "file containing the Server bearer token")
	productLine := flag.String("product-line", "", "explicit product line; data never crosses this boundary")
	businessDate := flag.String("business-date", "", "business date in YYYY-MM-DD; defaults to yesterday")
	timezone := flag.String("timezone", "Asia/Shanghai", "IANA timezone for the business date")
	outputRoot := flag.String("output-root", "out/iterations", "immutable iteration artifact root")
	runID := flag.String("run-id", "", "stable run identifier; defaults to date and current UTC time")
	packManifest := flag.String("pack-manifest", "config/cora-base-v0.json", "local reviewed Pack manifest")
	codeEvidence := flag.String("code-evidence", "", "optional Atlas evidence JSONL, explicitly scoped by product line and fingerprint")
	pageSize := flag.Int("case-page-size", 100, "case export page size from 1 to 200")
	attentionLimit := flag.Int("attention-limit", 200, "current attention incident limit from 1 to 200")
	baselineDays := flag.Int("baseline-days", 7, "complete days preceding the business date")
	frequencyMinimum := flag.Int64("frequency-minimum", 20, "minimum ignore occurrences before escalation review")
	frequencyRatio := flag.Float64("frequency-ratio", 3, "ignore frequency ratio over prior daily average")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()

	if *showVersion {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}
	if *serverURL == "" || *tokenFile == "" || *productLine == "" {
		fatal(fmt.Errorf("server-url, auth-token-file, and product-line are required"))
	}
	location, err := time.LoadLocation(*timezone)
	if err != nil {
		fatal(fmt.Errorf("load timezone: %w", err))
	}
	if *businessDate == "" {
		*businessDate = time.Now().In(location).AddDate(0, 0, -1).Format("2006-01-02")
	}
	token, err := auth.LoadBearerTokenFile(*tokenFile)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	source, err := iteration.NewHTTPSource(connectCtx, *serverURL, token)
	connectCancel()
	if err != nil {
		fatal(err)
	}
	defer source.Close()

	result, err := iteration.RunIteration(ctx, source, iteration.Config{
		ProductLine: *productLine, BusinessDate: *businessDate, Location: location,
		OutputRoot: filepath.Clean(*outputRoot), RunID: *runID,
		PackManifestPath: filepath.Clean(*packManifest), PageSize: *pageSize,
		CodeEvidencePath: cleanOptional(*codeEvidence),
		AttentionLimit:   *attentionLimit, BaselineDays: *baselineDays,
		FrequencyMinimum: *frequencyMinimum, FrequencyRatioThreshold: *frequencyRatio,
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

func cleanOptional(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
