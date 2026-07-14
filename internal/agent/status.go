package agent

import (
	"sync"
	"time"
)

type TargetStatus struct {
	ProductLine      string     `json:"product_line"`
	Service          string     `json:"service"`
	Path             string     `json:"path"`
	Running          bool       `json:"running"`
	Readable         bool       `json:"readable"`
	FileSizeBytes    int64      `json:"file_size_bytes"`
	CommittedOffset  int64      `json:"committed_offset"`
	LagBytes         int64      `json:"lag_bytes"`
	ParsedRecords    uint64     `json:"parsed_records"`
	ParseFailures    uint64     `json:"parse_failures"`
	ErrorEvents      uint64     `json:"error_events"`
	SentEvents       uint64     `json:"sent_events"`
	RetryAttempts    uint64     `json:"retry_attempts"`
	DeliveryFailures uint64     `json:"delivery_failures"`
	DroppedEvents    uint64     `json:"dropped_events"`
	TruncatedRecords uint64     `json:"truncated_records"`
	DeliveryFailing  bool       `json:"delivery_failing"`
	LastReadAt       *time.Time `json:"last_read_at,omitempty"`
	LastDeliveryAt   *time.Time `json:"last_delivery_at,omitempty"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

type targetRuntime struct {
	mu     sync.Mutex
	status TargetStatus
}

func newTargetRuntime(cfg Config) *targetRuntime {
	return &targetRuntime{status: TargetStatus{
		ProductLine: cfg.ProductLine, Service: cfg.Service, Path: cfg.Path,
	}}
}

func (runtime *targetRuntime) snapshot() TargetStatus {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.status
}

func (runtime *targetRuntime) update(fn func(*TargetStatus)) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	fn(&runtime.status)
}

func (runtime *targetRuntime) setRunning(running bool) {
	runtime.update(func(status *TargetStatus) { status.Running = running })
}

func (runtime *targetRuntime) setPath(path string) {
	runtime.update(func(status *TargetStatus) { status.Path = path })
}

func (runtime *targetRuntime) setUnavailable(err error) {
	runtime.update(func(status *TargetStatus) {
		status.Readable = false
		now := time.Now().UTC()
		status.LastFailureAt = &now
		status.LastError = err.Error()
	})
}

func (runtime *targetRuntime) observeFile(size, committed int64) {
	runtime.update(func(status *TargetStatus) {
		status.Readable = true
		status.FileSizeBytes = size
		status.CommittedOffset = committed
		status.LagBytes = max(size-committed, 0)
		if !status.DeliveryFailing {
			status.LastError = ""
		}
	})
}

func (runtime *targetRuntime) readRecord(parsed, isError, truncated bool) {
	runtime.update(func(status *TargetStatus) {
		now := time.Now().UTC()
		status.LastReadAt = &now
		if parsed {
			status.ParsedRecords++
		} else {
			status.ParseFailures++
		}
		if isError {
			status.ErrorEvents++
		}
		if truncated {
			status.TruncatedRecords++
		}
	})
}

func (runtime *targetRuntime) retry(err error) {
	runtime.update(func(status *TargetStatus) {
		status.RetryAttempts++
		status.DeliveryFailing = true
		now := time.Now().UTC()
		status.LastFailureAt = &now
		status.LastError = err.Error()
	})
}

func (runtime *targetRuntime) deliveryFailed(err error) {
	runtime.update(func(status *TargetStatus) {
		status.DeliveryFailures++
		status.DeliveryFailing = true
		now := time.Now().UTC()
		status.LastFailureAt = &now
		status.LastError = err.Error()
	})
}

func (runtime *targetRuntime) delivered(events int) {
	runtime.update(func(status *TargetStatus) {
		status.SentEvents += uint64(events)
		status.DeliveryFailing = false
		now := time.Now().UTC()
		status.LastDeliveryAt = &now
		status.LastError = ""
	})
}
