package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/claracore/cora/internal/cora"
)

const (
	defaultBreadcrumbMaxBytes = 16 << 10
	traceBreadcrumbWindow     = 30 * time.Second
	threadBreadcrumbWindow    = 5 * time.Second
	traceBreadcrumbLimit      = 20
	threadBreadcrumbLimit     = 5
)

type breadcrumbEntry struct {
	traceID    string
	breadcrumb cora.Breadcrumb
}

type breadcrumbBuffer struct {
	maxBytes int
	entries  []breadcrumbEntry
}

func newBreadcrumbBuffer(maxBytes int) *breadcrumbBuffer {
	if maxBytes <= 0 {
		maxBytes = defaultBreadcrumbMaxBytes
	}
	return &breadcrumbBuffer{maxBytes: maxBytes}
}

func (buffer *breadcrumbBuffer) add(record parsedRecord) {
	if !retainBreadcrumb(record) {
		return
	}
	entry := breadcrumbEntry{traceID: record.traceID, breadcrumb: cora.Breadcrumb{
		OccurredAt: record.occurredAt, Level: record.level, Logger: record.logger,
		Thread: record.thread, Method: record.method, Line: record.line, Message: record.message,
	}}
	buffer.entries = append(buffer.entries, entry)
	for buffer.encodedSize() > buffer.maxBytes && len(buffer.entries) > 1 {
		buffer.entries = buffer.entries[1:]
	}
	if buffer.encodedSize() <= buffer.maxBytes {
		return
	}
	buffer.entries[0].breadcrumb.Message = "[truncated]"
	if buffer.encodedSize() > buffer.maxBytes {
		buffer.entries = buffer.entries[:0]
	}
}

func retainBreadcrumb(record parsedRecord) bool {
	if strings.HasSuffix(record.logger, "RequestResponseLoggingFilter") &&
		(record.method == "logRequest" || record.method == "logResponse") {
		return false
	}
	return true
}

func (buffer *breadcrumbBuffer) selectFor(record parsedRecord) []cora.Breadcrumb {
	window := threadBreadcrumbWindow
	limit := threadBreadcrumbLimit
	match := func(entry breadcrumbEntry) bool {
		return record.thread != "" && entry.breadcrumb.Thread == record.thread
	}
	if record.traceID != "" {
		window = traceBreadcrumbWindow
		limit = traceBreadcrumbLimit
		match = func(entry breadcrumbEntry) bool { return entry.traceID == record.traceID }
	}
	selected := make([]cora.Breadcrumb, 0, limit)
	for index := len(buffer.entries) - 1; index >= 0 && len(selected) < limit; index-- {
		entry := buffer.entries[index]
		age := record.occurredAt.Sub(entry.breadcrumb.OccurredAt)
		if age < 0 || age > window || !match(entry) {
			continue
		}
		selected = append(selected, entry.breadcrumb)
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

func (buffer *breadcrumbBuffer) encodedSize() int {
	wire := make([]struct {
		TraceID    string          `json:"trace_id,omitempty"`
		Breadcrumb cora.Breadcrumb `json:"breadcrumb"`
	}, len(buffer.entries))
	for index, entry := range buffer.entries {
		wire[index].TraceID = entry.traceID
		wire[index].Breadcrumb = entry.breadcrumb
	}
	encoded, _ := json.Marshal(wire)
	return len(encoded)
}
