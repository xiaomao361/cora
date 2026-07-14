package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/claracore/cora/internal/agent"
	"github.com/claracore/cora/internal/auth"
	"github.com/claracore/cora/internal/buildinfo"
)

func main() {
	cfg := agent.Config{}
	configFile := flag.String("config.file", "", "Promtail-style YAML configuration file")
	checkConfig := flag.Bool("check-config", false, "validate config.file and exit")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	authTokenFile := flag.String("auth-token-file", "", "file containing the Server bearer token")
	flag.StringVar(&cfg.Path, "file", "", "active log file to follow")
	flag.StringVar(&cfg.PositionsPath, "positions", "./cora-agent-positions.json", "durable positions file")
	flag.StringVar(&cfg.Endpoint, "endpoint", "http://127.0.0.1:8080/v1/events:batch", "Cora batch ingest endpoint")
	flag.StringVar(&cfg.ProductLine, "product-line", "", "Cora product line")
	flag.StringVar(&cfg.Service, "service", "", "service identity")
	flag.StringVar(&cfg.Environment, "environment", "prod", "deployment environment")
	flag.StringVar(&cfg.Release, "release", "", "optional release identity")
	flag.StringVar(&cfg.Timezone, "timezone", "Local", "timezone for timestamps without an offset")
	flag.BoolVar(&cfg.StartAtBeginning, "from-start", false, "read an unseen file from the beginning instead of its current end")
	flag.IntVar(&cfg.BatchSize, "batch-size", 100, "events per request, 1..500")
	flag.IntVar(&cfg.MaxEventBytes, "max-event-bytes", 256<<10, "maximum retained bytes for one multiline event")
	flag.IntVar(&cfg.MaxBatchBytes, "max-batch-bytes", 1536<<10, "maximum JSON request bytes, at most 2 MiB")
	flag.DurationVar(&cfg.BatchWait, "batch-wait", time.Second, "maximum multiline quiet time and batch delay")
	flag.DurationVar(&cfg.PollInterval, "poll-interval", 250*time.Millisecond, "file follow and rotation poll interval")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 3*time.Second, "HTTP request timeout")
	flag.IntVar(&cfg.MaxRetries, "max-retries", 5, "retries for connection, 429, and 5xx failures")
	flag.DurationVar(&cfg.MinBackoff, "min-backoff", 250*time.Millisecond, "initial retry backoff")
	flag.DurationVar(&cfg.MaxBackoff, "max-backoff", 5*time.Second, "maximum retry backoff")
	flag.Parse()
	if *showVersion {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if *configFile != "" {
		var runtime agent.RuntimeConfig
		runtime, err = agent.LoadConfig(*configFile)
		if err == nil && *authTokenFile != "" {
			var token string
			token, err = auth.LoadBearerTokenFile(*authTokenFile)
			if err == nil {
				for index := range runtime.Targets {
					runtime.Targets[index].BearerToken = token
				}
			}
		}
		if err == nil && *checkConfig {
			fmt.Printf("configuration valid: %d targets\n", len(runtime.Targets))
			return
		}
		if err == nil {
			err = agent.RunMulti(ctx, runtime)
		}
	} else {
		if *checkConfig {
			fmt.Fprintln(os.Stderr, "check-config requires config.file")
			os.Exit(2)
		}
		if *authTokenFile != "" {
			cfg.BearerToken, err = auth.LoadBearerTokenFile(*authTokenFile)
		}
		if err == nil {
			err = agent.Run(ctx, cfg)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
