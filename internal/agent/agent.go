package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/cora"
	"github.com/claracore/cora/internal/sanitize"
)

type Config struct {
	Path             string
	PositionsPath    string
	Endpoint         string
	BearerToken      string
	ProductLine      string
	Service          string
	Environment      string
	Release          string
	Labels           map[string]string
	Timezone         string
	StartAtBeginning bool
	BatchSize        int
	MaxEventBytes    int
	MaxBatchBytes    int
	BatchWait        time.Duration
	PollInterval     time.Duration
	RequestTimeout   time.Duration
	MaxRetries       int
	MinBackoff       time.Duration
	MaxBackoff       time.Duration
}

type queuedEvent struct {
	event cora.Event
	end   int64
}

var errReopen = errors.New("reopen active log file")

func (cfg Config) validate() error {
	if cfg.Path == "" || cfg.PositionsPath == "" || cfg.Endpoint == "" ||
		cfg.ProductLine == "" || cfg.Service == "" || cfg.Environment == "" {
		return errors.New("path, positions, endpoint, product-line, service, and environment are required")
	}
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("endpoint must be an absolute HTTP(S) URL")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 500 || cfg.MaxEventBytes < 1024 ||
		cfg.MaxBatchBytes < cfg.MaxEventBytes || cfg.MaxBatchBytes > 2<<20 ||
		cfg.BatchWait <= 0 || cfg.PollInterval <= 0 ||
		cfg.RequestTimeout <= 0 || cfg.MaxRetries < 0 || cfg.MinBackoff <= 0 || cfg.MaxBackoff < cfg.MinBackoff {
		return errors.New("invalid batch, polling, timeout, or retry configuration")
	}
	if cfg.Timezone != "" && cfg.Timezone != "Local" {
		if _, err := time.LoadLocation(cfg.Timezone); err != nil {
			return fmt.Errorf("load timezone: %w", err)
		}
	}
	return nil
}

func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	build := buildinfo.Current()
	log.Printf("Cora Agent starting mode=single version=%s commit=%s targets=1", build.Version, build.Commit)
	positions, err := openPositionStore(cfg.PositionsPath)
	if err != nil {
		return err
	}
	return runTarget(ctx, cfg, positions, newTargetRuntime(cfg))
}

func runTarget(ctx context.Context, cfg Config, positions *positionStore, runtime *targetRuntime) error {
	absolutePath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return err
	}
	runtime.setPath(absolutePath)
	runtime.setRunning(true)
	log.Printf("Cora Agent target starting product_line=%q service=%q path=%q", cfg.ProductLine, cfg.Service, absolutePath)
	defer func() {
		runtime.setRunning(false)
		status := runtime.snapshot()
		log.Printf("Cora Agent target stopped product_line=%q service=%q parsed=%d errors=%d sent=%d retries=%d failures=%d lag_bytes=%d",
			cfg.ProductLine, cfg.Service, status.ParsedRecords, status.ErrorEvents, status.SentEvents,
			status.RetryAttempts, status.DeliveryFailures, status.LagBytes)
	}()
	client := &http.Client{Timeout: cfg.RequestTimeout}
	breadcrumbs := newBreadcrumbBuffer(defaultBreadcrumbMaxBytes)
	unavailableLogged := false
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := tailOpenFile(ctx, cfg, absolutePath, positions, client, breadcrumbs, runtime)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errReopen) {
			unavailableLogged = false
			continue
		}
		if err == nil {
			return nil
		}
		runtime.setUnavailable(err)
		if !unavailableLogged {
			log.Printf("Cora Agent target unavailable product_line=%q service=%q path=%q error=%q",
				cfg.ProductLine, cfg.Service, absolutePath, sanitize.RedactSignedURLCredentials(err.Error()))
			unavailableLogged = true
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := wait(ctx, cfg.PollInterval); err != nil {
			return nil
		}
	}
}

func tailOpenFile(ctx context.Context, cfg Config, path string, positions *positionStore, client *http.Client, breadcrumbs *breadcrumbBuffer, runtime *targetRuntime) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	identity := fileID(info)
	stored, exists := positions.get(path)
	offset := int64(0)
	startMode := "beginning"
	if exists && stored.FileID == identity && stored.Offset <= info.Size() {
		offset = stored.Offset
		startMode = "resume"
	} else if !exists && !cfg.StartAtBeginning {
		offset = info.Size()
		startMode = "tail"
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	currentPosition := position{Offset: offset, FileID: identity}
	if err := positions.put(path, currentPosition); err != nil {
		return err
	}
	runtime.observeFile(info.Size(), offset)
	log.Printf("Cora Agent target file opened product_line=%q service=%q path=%q mode=%s offset=%d size=%d",
		cfg.ProductLine, cfg.Service, path, startMode, offset, info.Size())

	reader := bufio.NewReader(file)
	var record strings.Builder
	lastRead := time.Now()
	pending := make([]queuedEvent, 0, cfg.BatchSize)
	processedEnd := offset
	recordTruncated := false

	finalize := func(end int64) {
		if record.Len() == 0 {
			processedEnd = end
			return
		}
		parsedRecord, parsed := parseLogbackRecord(record.String(), cfg)
		isError := false
		if parsed {
			if event, eventIsError := eventFromRecord(parsedRecord, path, cfg); eventIsError {
				isError = true
				event.Breadcrumbs = breadcrumbs.selectFor(parsedRecord)
				pending = append(pending, queuedEvent{event: event, end: end})
			}
			breadcrumbs.add(parsedRecord)
		}
		runtime.readRecord(parsed, isError, recordTruncated)
		processedEnd = end
		record.Reset()
		recordTruncated = false
	}
	flush := func() error {
		for len(pending) > 0 {
			count, events, err := nextBatch(pending, cfg.BatchSize, cfg.MaxBatchBytes)
			if err != nil {
				return err
			}
			if err := sendBatch(ctx, client, cfg, events, runtime); err != nil {
				return err
			}
			committed := pending[count-1].end
			currentPosition = position{Offset: committed, FileID: identity}
			if err := positions.put(path, currentPosition); err != nil {
				return err
			}
			runtime.observeFile(max(offset, committed), committed)
			pending = pending[count:]
		}
		currentPosition = position{Offset: processedEnd, FileID: identity}
		if err := positions.put(path, currentPosition); err != nil {
			return err
		}
		runtime.observeFile(max(offset, processedEnd), processedEnd)
		pending = pending[:0]
		return nil
	}

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			lineStart := offset
			offset += int64(len(line))
			lastRead = time.Now()
			trimmed := strings.TrimRight(line, "\r\n")
			if startsRecord(trimmed) {
				finalize(lineStart)
				recordTruncated = appendBounded(&record, trimmed, cfg.MaxEventBytes, false)
			} else if record.Len() > 0 {
				recordTruncated = appendBounded(&record, trimmed, cfg.MaxEventBytes, true) || recordTruncated
			} else {
				processedEnd = offset
			}
			if len(pending) >= cfg.BatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read log file: %w", readErr)
		}
		if !errors.Is(readErr, io.EOF) {
			continue
		}
		if record.Len() > 0 && time.Since(lastRead) >= cfg.BatchWait {
			finalize(offset)
		}
		if record.Len() == 0 && processedEnd > currentPosition.Offset {
			if err := flush(); err != nil {
				return err
			}
		}

		pathInfo, statErr := os.Stat(path)
		openInfo, openErr := file.Stat()
		if statErr == nil {
			runtime.observeFile(pathInfo.Size(), currentPosition.Offset)
		}
		if statErr == nil && openErr == nil && (!os.SameFile(openInfo, pathInfo) || pathInfo.Size() < offset) {
			reason := "rotation"
			if os.SameFile(openInfo, pathInfo) && pathInfo.Size() < offset {
				reason = "copy-truncate"
			}
			log.Printf("Cora Agent target reopening product_line=%q service=%q path=%q reason=%s",
				cfg.ProductLine, cfg.Service, path, reason)
			finalize(offset)
			if err := flush(); err != nil {
				return err
			}
			return errReopen
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if err := wait(ctx, cfg.PollInterval); err != nil {
			return nil
		}
	}
}

func appendBounded(record *strings.Builder, line string, maximum int, newline bool) bool {
	remaining := maximum - record.Len()
	if remaining <= 0 {
		return true
	}
	if newline {
		record.WriteByte('\n')
		remaining--
	}
	if remaining <= 0 {
		return true
	}
	truncated := len(line) > remaining
	if len(line) > remaining {
		line = line[:remaining]
	}
	record.WriteString(line)
	return truncated
}

func nextBatch(pending []queuedEvent, maximumCount, maximumBytes int) (int, []cora.Event, error) {
	limit := min(len(pending), maximumCount)
	for count := 1; count <= limit; count++ {
		events := make([]cora.Event, count)
		for index := range events {
			events[index] = redactEvent(pending[index].event)
		}
		body, err := json.Marshal(struct {
			Events []cora.Event `json:"events"`
		}{Events: events})
		if err != nil {
			return 0, nil, err
		}
		if len(body) > maximumBytes {
			if count == 1 {
				return 0, nil, fmt.Errorf("single event exceeds max batch bytes")
			}
			return count - 1, events[:count-1], nil
		}
		if count == limit {
			return count, events, nil
		}
	}
	return 0, nil, errors.New("empty batch")
}

func sendBatch(ctx context.Context, client *http.Client, cfg Config, events []cora.Event, runtime *targetRuntime) error {
	for index := range events {
		events[index] = redactEvent(events[index])
	}
	body, err := json.Marshal(struct {
		Events []cora.Event `json:"events"`
	}{Events: events})
	if err != nil {
		return err
	}
	backoff := cfg.MinBackoff
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("content-type", "application/json")
		if cfg.BearerToken != "" {
			request.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
		}
		response, err := client.Do(request)
		status := 0
		if response != nil {
			status = response.StatusCode
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
		}
		if err == nil && status >= 200 && status < 300 {
			runtime.delivered(len(events))
			log.Printf("Cora Agent batch delivered product_line=%q service=%q events=%d bytes=%d status=%d attempts=%d",
				cfg.ProductLine, cfg.Service, len(events), len(body), status, attempt+1)
			return nil
		}
		retryable := err != nil || status == http.StatusTooManyRequests || status >= 500
		attemptErr := fmt.Errorf("delivery attempt %d failed: status=%d error=%v", attempt+1, status, err)
		if !retryable || attempt >= cfg.MaxRetries {
			finalErr := fmt.Errorf("send batch failed after %d attempts: status=%d error=%v", attempt+1, status, err)
			runtime.deliveryFailed(finalErr)
			log.Printf("Cora Agent batch failed product_line=%q service=%q events=%d status=%d attempts=%d error=%q",
				cfg.ProductLine, cfg.Service, len(events), status, attempt+1,
				sanitize.RedactSignedURLCredentials(fmt.Sprint(err)))
			return finalErr
		}
		runtime.retry(attemptErr)
		log.Printf("Cora Agent batch retry product_line=%q service=%q events=%d status=%d attempt=%d next_backoff=%s error=%q",
			cfg.ProductLine, cfg.Service, len(events), status, attempt+1, backoff,
			sanitize.RedactSignedURLCredentials(fmt.Sprint(err)))
		if err := wait(ctx, backoff); err != nil {
			return err
		}
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
