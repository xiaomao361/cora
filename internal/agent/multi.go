package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
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
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

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
			json.NewEncoder(writer).Encode(map[string]any{"status": "ok", "targets": len(runtime.Targets)})
		})
		mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
			readable, unavailable := targetAvailability(runtime.Targets)
			status := "ready"
			code := http.StatusOK
			if len(unavailable) > 0 {
				status = "degraded"
				code = http.StatusServiceUnavailable
			}
			writer.Header().Set("content-type", "application/json")
			writer.WriteHeader(code)
			json.NewEncoder(writer).Encode(map[string]any{
				"status": status, "targets": len(runtime.Targets),
				"readable_targets": readable, "unavailable_services": unavailable,
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
	for _, target := range runtime.Targets {
		target := target
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := runTarget(runCtx, target, positions); err != nil && runCtx.Err() == nil {
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
	return result
}

func targetAvailability(targets []Config) (int, []string) {
	readable := 0
	unavailable := make([]string, 0)
	for _, target := range targets {
		file, err := os.Open(target.Path)
		if err != nil {
			unavailable = append(unavailable, target.Service)
			continue
		}
		file.Close()
		readable++
	}
	return readable, unavailable
}
