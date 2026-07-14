package serverconfig

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Runtime struct {
	HTTPListenAddress    string
	HTTPListenPort       int
	DatabasePath         string
	BearerTokenFile      string
	AllowUnauthenticated bool
	FlushInterval        time.Duration
	MaxActive            int
}

func (runtime Runtime) Address() string {
	return net.JoinHostPort(runtime.HTTPListenAddress, strconv.Itoa(runtime.HTTPListenPort))
}

type fileConfig struct {
	Server      serverBlock      `yaml:"server"`
	Storage     storageBlock     `yaml:"storage"`
	Auth        authBlock        `yaml:"auth"`
	Aggregation aggregationBlock `yaml:"aggregation"`
}

type serverBlock struct {
	HTTPListenAddress string `yaml:"http_listen_address"`
	HTTPListenPort    int    `yaml:"http_listen_port"`
}

type storageBlock struct {
	Path string `yaml:"path"`
}

type authBlock struct {
	BearerTokenFile      string `yaml:"bearer_token_file"`
	AllowUnauthenticated bool   `yaml:"allow_unauthenticated"`
}

type aggregationBlock struct {
	FlushInterval string `yaml:"flush_interval"`
	MaxActive     int    `yaml:"max_active"`
}

func Load(path string) (Runtime, error) {
	file, err := os.Open(path)
	if err != nil {
		return Runtime{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return Runtime{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(data))))
	decoder.KnownFields(true)
	var source fileConfig
	if err := decoder.Decode(&source); err != nil {
		return Runtime{}, fmt.Errorf("parse server config: %w", err)
	}
	return source.runtime()
}

func (source fileConfig) runtime() (Runtime, error) {
	result := Runtime{
		HTTPListenAddress:    source.Server.HTTPListenAddress,
		HTTPListenPort:       source.Server.HTTPListenPort,
		DatabasePath:         source.Storage.Path,
		BearerTokenFile:      source.Auth.BearerTokenFile,
		AllowUnauthenticated: source.Auth.AllowUnauthenticated,
		MaxActive:            source.Aggregation.MaxActive,
	}
	if result.HTTPListenAddress == "" {
		result.HTTPListenAddress = "127.0.0.1"
	}
	if result.HTTPListenPort == 0 {
		result.HTTPListenPort = 8080
	}
	if result.DatabasePath == "" {
		result.DatabasePath = "./cora.db"
	}
	if result.MaxActive == 0 {
		result.MaxActive = 10000
	}
	flushInterval := source.Aggregation.FlushInterval
	if flushInterval == "" {
		flushInterval = "10s"
	}
	var err error
	result.FlushInterval, err = time.ParseDuration(flushInterval)
	if err != nil {
		return Runtime{}, fmt.Errorf("aggregation.flush_interval: %w", err)
	}
	if result.HTTPListenPort < 1 || result.HTTPListenPort > 65535 {
		return Runtime{}, errors.New("server.http_listen_port must be between 1 and 65535")
	}
	if result.FlushInterval <= 0 || result.MaxActive < 1 {
		return Runtime{}, errors.New("aggregation.flush_interval and aggregation.max_active must be positive")
	}
	if result.BearerTokenFile == "" && !result.AllowUnauthenticated {
		return Runtime{}, errors.New("auth.bearer_token_file is required unless auth.allow_unauthenticated is true")
	}
	return result, nil
}
