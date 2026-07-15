package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
)

func RunMulti(ctx context.Context, runtime RuntimeConfig) error {
	if len(runtime.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	positionsPath := runtime.Targets[0].PositionsPath
	for _, target := range runtime.Targets {
		if target.PositionsPath != positionsPath {
			return errors.New("all targets must share one positions file")
		}
		if err := target.validate(); err != nil {
			return err
		}
	}
	positions, err := openPositionStore(positionsPath)
	if err != nil {
		return err
	}
	build := buildinfo.Current()
	log.Printf("Cora Agent starting mode=multi version=%s commit=%s targets=%d health=%s:%d",
		build.Version, build.Commit, len(runtime.Targets), runtime.Health.Address, runtime.Health.Port)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runtimes := make([]*targetRuntime, len(runtime.Targets))
	for index, target := range runtime.Targets {
		runtimes[index] = newTargetRuntime(target)
	}

	var server *http.Server
	serverErrors := make(chan error, 1)
	if runtime.Health.Port > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", runtime.Health.Address, runtime.Health.Port))
		if err != nil {
			return fmt.Errorf("listen agent health: %w", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("content-type", "application/json")
			json.NewEncoder(writer).Encode(map[string]any{
				"status": "ok", "targets": len(runtimes),
				"target_statuses": runtimeSnapshots(runtimes), "build": buildinfo.Current(),
			})
		})
		mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
			ready, reasons := runtimeReadiness(runtimes)
			status := "ready"
			code := http.StatusOK
			if !ready {
				status = "degraded"
				code = http.StatusServiceUnavailable
			}
			writer.Header().Set("content-type", "application/json")
			writer.WriteHeader(code)
			json.NewEncoder(writer).Encode(map[string]any{
				"status": status, "reasons": reasons, "targets": len(runtimes),
				"readable_targets": readableRuntimeCount(runtimes),
				"target_statuses":  runtimeSnapshots(runtimes), "build": buildinfo.Current(),
			})
		})
		server = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrors <- err
			}
		}()
	}

	targetErrors := make(chan error, len(runtime.Targets))
	var workers sync.WaitGroup
	for index, target := range runtime.Targets {
		target := target
		targetRuntime := runtimes[index]
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := runTarget(runCtx, target, positions, targetRuntime); err != nil && runCtx.Err() == nil {
				targetErrors <- fmt.Errorf("target %s (%s): %w", target.Service, target.Path, err)
			}
		}()
	}

	var result error
	select {
	case <-ctx.Done():
	case result = <-targetErrors:
	case result = <-serverErrors:
	}
	cancel()
	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		server.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	workers.Wait()
	resultText := "none"
	if result != nil {
		resultText = result.Error()
	}
	log.Printf("Cora Agent stopped mode=multi targets=%d error=%q", len(runtime.Targets), resultText)
	return result
}

func runtimeSnapshots(runtimes []*targetRuntime) []TargetStatus {
	result := make([]TargetStatus, 0, len(runtimes))
	for _, runtime := range runtimes {
		result = append(result, runtime.snapshot())
	}
	return result
}

func runtimeReadiness(runtimes []*targetRuntime) (bool, []string) {
	reasons := []string{}
	for _, runtime := range runtimes {
		status := runtime.snapshot()
		if !status.Running {
			reasons = append(reasons, status.Service+": worker is not running")
		}
		if !status.Readable {
			reasons = append(reasons, status.Service+": log file is not readable")
		}
		if status.DeliveryFailing {
			reasons = append(reasons, status.Service+": delivery is failing")
		}
	}
	return len(reasons) == 0, reasons
}

func readableRuntimeCount(runtimes []*targetRuntime) int {
	count := 0
	for _, runtime := range runtimes {
		if runtime.snapshot().Readable {
			count++
		}
	}
	return count
}
