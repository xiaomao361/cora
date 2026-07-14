package agent

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/claracore/cora/internal/cora"
)

var (
	logbackHeader = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3})\s+trace_id:\s*(\S*)\s+\[([^]]*)]\s+([A-Z]+)\s+(\S+)\s+-\s+\[([^,]*),([^]]*)]\s+-\s?(.*)$`)
	exceptionLine = regexp.MustCompile(`^(?:Caused by:\s*)?([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)+)(?::|$)`)
)

type parsedRecord struct {
	occurredAt time.Time
	traceID    string
	thread     string
	level      string
	logger     string
	method     string
	line       string
	message    string
	continued  []string
}

func startsRecord(line string) bool { return logbackHeader.MatchString(line) }

func parseLogbackRecord(text string, cfg Config) (parsedRecord, bool) {
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	if len(lines) == 0 {
		return parsedRecord{}, false
	}
	parts := logbackHeader.FindStringSubmatch(strings.TrimSuffix(lines[0], "\r"))
	if len(parts) != 9 {
		return parsedRecord{}, false
	}
	record := parsedRecord{
		traceID: parts[2], thread: parts[3], level: parts[4], logger: parts[5],
		method: parts[6], line: parts[7], message: parts[8],
	}
	for _, line := range lines[1:] {
		record.continued = append(record.continued, strings.TrimSuffix(line, "\r"))
	}
	location := time.Local
	if cfg.Timezone != "" && cfg.Timezone != "Local" {
		if loaded, err := time.LoadLocation(cfg.Timezone); err == nil {
			location = loaded
		}
	}
	occurredAt, err := time.ParseInLocation("2006-01-02 15:04:05.000", parts[1], location)
	if err != nil {
		return parsedRecord{}, false
	}
	record.occurredAt = occurredAt
	return record, true
}

func parseRecord(text, source string, cfg Config) (cora.Event, bool) {
	record, ok := parseLogbackRecord(text, cfg)
	if !ok {
		return cora.Event{}, false
	}
	return eventFromRecord(record, source, cfg)
}

func eventFromRecord(record parsedRecord, source string, cfg Config) (cora.Event, bool) {
	if record.level != "ERROR" {
		return cora.Event{}, false
	}
	exceptionType := "logback.ERROR"
	for _, line := range record.continued {
		trimmed := strings.TrimSpace(line)
		if match := exceptionLine.FindStringSubmatch(trimmed); len(match) == 2 {
			exceptionType = match[1]
			break
		}
	}
	stacktrace := strings.Join(record.continued, "\n")
	if stacktrace == "" && record.method != "" && record.method != "?" {
		stacktrace = "at " + record.logger + "." + record.method + "(Unknown Source)"
	}
	return cora.Event{
		ProductLine: cfg.ProductLine, Service: cfg.Service, Environment: cfg.Environment,
		Release: cfg.Release, Source: filepath.Base(source), TraceID: record.traceID, Labels: cfg.Labels,
		Thread: record.thread, Method: record.method, Line: record.line,
		Logger: record.logger, ExceptionType: exceptionType, Message: record.message,
		Stacktrace: stacktrace, OccurredAt: record.occurredAt,
	}, true
}
