package agent

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/claracore/cora/internal/auth"
	"gopkg.in/yaml.v3"
)

type RuntimeConfig struct {
	Health  HealthConfig
	Targets []Config
}

type HealthConfig struct {
	Address string
	Port    int
}

type fileConfig struct {
	Server        serverConfig    `yaml:"server"`
	Positions     positionsConfig `yaml:"positions"`
	Clients       []clientConfig  `yaml:"clients"`
	Defaults      defaultsConfig  `yaml:"defaults"`
	Agent         agentOptions    `yaml:"agent"`
	ScrapeConfigs []scrapeConfig  `yaml:"scrape_configs"`
}

type serverConfig struct {
	HTTPListenAddress string `yaml:"http_listen_address"`
	HTTPListenPort    int    `yaml:"http_listen_port"`
	GRPCListenPort    int    `yaml:"grpc_listen_port"`
}

type positionsConfig struct {
	Filename string `yaml:"filename"`
}

type clientConfig struct {
	URL             string `yaml:"url"`
	BearerTokenFile string `yaml:"bearer_token_file"`
}

type defaultsConfig struct {
	ProductLine string `yaml:"product_line"`
	Environment string `yaml:"environment"`
	Release     string `yaml:"release"`
	Timezone    string `yaml:"timezone"`
}

type agentOptions struct {
	StartAt        string `yaml:"start_at"`
	BatchSize      int    `yaml:"batch_size"`
	MaxEventBytes  int    `yaml:"max_event_bytes"`
	MaxBatchBytes  int    `yaml:"max_batch_bytes"`
	BatchWait      string `yaml:"batch_wait"`
	PollInterval   string `yaml:"poll_interval"`
	RequestTimeout string `yaml:"request_timeout"`
	MaxRetries     *int   `yaml:"max_retries"`
	MinBackoff     string `yaml:"min_backoff"`
	MaxBackoff     string `yaml:"max_backoff"`
}

type scrapeConfig struct {
	JobName       string         `yaml:"job_name"`
	StaticConfigs []staticConfig `yaml:"static_configs"`
}

type staticConfig struct {
	Targets []string          `yaml:"targets"`
	Labels  map[string]string `yaml:"labels"`
}

func LoadConfig(path string) (RuntimeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return RuntimeConfig{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(data))))
	decoder.KnownFields(true)
	var source fileConfig
	if err := decoder.Decode(&source); err != nil {
		return RuntimeConfig{}, fmt.Errorf("parse agent config: %w", err)
	}
	return source.runtime()
}

func (source fileConfig) runtime() (RuntimeConfig, error) {
	if source.Server.GRPCListenPort != 0 {
		return RuntimeConfig{}, errors.New("grpc_listen_port is unsupported and must be 0")
	}
	if source.Positions.Filename == "" {
		return RuntimeConfig{}, errors.New("positions.filename is required")
	}
	if len(source.Clients) != 1 || source.Clients[0].URL == "" {
		return RuntimeConfig{}, errors.New("exactly one clients[].url is required")
	}
	endpoint, err := url.Parse(source.Clients[0].URL)
	if err != nil || !strings.HasSuffix(endpoint.Path, "/v1/events:batch") {
		return RuntimeConfig{}, errors.New("clients[0].url must point to Cora /v1/events:batch")
	}
	if source.Defaults.ProductLine == "" {
		return RuntimeConfig{}, errors.New("defaults.product_line is required")
	}
	bearerToken := ""
	if source.Clients[0].BearerTokenFile != "" {
		bearerToken, err = auth.LoadBearerTokenFile(source.Clients[0].BearerTokenFile)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("clients[0].bearer_token_file: %w", err)
		}
	}

	options, err := source.Agent.withDefaults()
	if err != nil {
		return RuntimeConfig{}, err
	}
	environment := source.Defaults.Environment
	if environment == "" {
		environment = "prod"
	}
	timezone := source.Defaults.Timezone
	if timezone == "" {
		timezone = "Local"
	}
	runtime := RuntimeConfig{Health: HealthConfig{
		Address: source.Server.HTTPListenAddress,
		Port:    source.Server.HTTPListenPort,
	}}
	if runtime.Health.Address == "" {
		runtime.Health.Address = "127.0.0.1"
	}
	paths := make(map[string]string)
	for _, scrape := range source.ScrapeConfigs {
		if scrape.JobName == "" {
			return RuntimeConfig{}, errors.New("scrape_configs[].job_name is required")
		}
		for _, static := range scrape.StaticConfigs {
			path := static.Labels["__path__"]
			service := static.Labels["app"]
			if path == "" || service == "" {
				return RuntimeConfig{}, fmt.Errorf("job %q requires labels.app and labels.__path__", scrape.JobName)
			}
			if strings.ContainsAny(path, "*?[]{}") {
				return RuntimeConfig{}, fmt.Errorf("job %q uses unsupported path glob %q", scrape.JobName, path)
			}
			if previous, exists := paths[path]; exists {
				return RuntimeConfig{}, fmt.Errorf("jobs %q and %q use the same path %q", previous, scrape.JobName, path)
			}
			paths[path] = scrape.JobName
			labels := make(map[string]string, len(static.Labels))
			for key, value := range static.Labels {
				if key != "__path__" {
					labels[key] = value
				}
			}
			labels["job"] = scrape.JobName
			targetEnvironment := static.Labels["env"]
			if targetEnvironment == "" {
				targetEnvironment = environment
			}
			release := static.Labels["release"]
			if release == "" {
				release = source.Defaults.Release
			}
			productLine := static.Labels["product_line"]
			if productLine == "" {
				productLine = source.Defaults.ProductLine
			}
			target := Config{
				Path: path, PositionsPath: source.Positions.Filename, Endpoint: source.Clients[0].URL,
				BearerToken: bearerToken,
				ProductLine: productLine, Service: service, Environment: targetEnvironment,
				Release: release, Labels: labels, Timezone: timezone,
				StartAtBeginning: options.startAtBeginning, BatchSize: options.batchSize,
				MaxEventBytes: options.maxEventBytes, MaxBatchBytes: options.maxBatchBytes,
				BatchWait: options.batchWait, PollInterval: options.pollInterval,
				RequestTimeout: options.requestTimeout, MaxRetries: options.maxRetries,
				MinBackoff: options.minBackoff, MaxBackoff: options.maxBackoff,
			}
			if err := target.validate(); err != nil {
				return RuntimeConfig{}, fmt.Errorf("job %q: %w", scrape.JobName, err)
			}
			runtime.Targets = append(runtime.Targets, target)
		}
	}
	if len(runtime.Targets) == 0 {
		return RuntimeConfig{}, errors.New("at least one static log target is required")
	}
	return runtime, nil
}

type resolvedOptions struct {
	startAtBeginning                                                bool
	batchSize, maxEventBytes, maxBatchBytes, maxRetries             int
	batchWait, pollInterval, requestTimeout, minBackoff, maxBackoff time.Duration
}

func (options agentOptions) withDefaults() (resolvedOptions, error) {
	result := resolvedOptions{batchSize: 100, maxEventBytes: 256 << 10, maxBatchBytes: 1536 << 10,
		maxRetries: 5, batchWait: time.Second, pollInterval: 250 * time.Millisecond,
		requestTimeout: 3 * time.Second, minBackoff: 250 * time.Millisecond, maxBackoff: 5 * time.Second}
	if options.StartAt != "" && options.StartAt != "end" && options.StartAt != "beginning" {
		return result, errors.New("agent.start_at must be end or beginning")
	}
	result.startAtBeginning = options.StartAt == "beginning"
	if options.BatchSize != 0 {
		result.batchSize = options.BatchSize
	}
	if options.MaxEventBytes != 0 {
		result.maxEventBytes = options.MaxEventBytes
	}
	if options.MaxBatchBytes != 0 {
		result.maxBatchBytes = options.MaxBatchBytes
	}
	if options.MaxRetries != nil {
		result.maxRetries = *options.MaxRetries
	}
	var err error
	if result.batchWait, err = durationOrDefault(options.BatchWait, result.batchWait); err != nil {
		return result, fmt.Errorf("agent.batch_wait: %w", err)
	}
	if result.pollInterval, err = durationOrDefault(options.PollInterval, result.pollInterval); err != nil {
		return result, fmt.Errorf("agent.poll_interval: %w", err)
	}
	if result.requestTimeout, err = durationOrDefault(options.RequestTimeout, result.requestTimeout); err != nil {
		return result, fmt.Errorf("agent.request_timeout: %w", err)
	}
	if result.minBackoff, err = durationOrDefault(options.MinBackoff, result.minBackoff); err != nil {
		return result, fmt.Errorf("agent.min_backoff: %w", err)
	}
	if result.maxBackoff, err = durationOrDefault(options.MaxBackoff, result.maxBackoff); err != nil {
		return result, fmt.Errorf("agent.max_backoff: %w", err)
	}
	return result, nil
}

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
