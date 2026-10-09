package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Service repeatedly invokes the processor as a managed runtime. The one-shot
// Runner remains available for diagnostics, while this type is the production
// automatic pickup loop.
type Service struct {
	Run          func(context.Context) error
	PollInterval time.Duration
	// Monitor, when set, is served on /status.
	Monitor *Monitor

	mu    sync.RWMutex
	ready bool
}

func (s *Service) Serve(ctx context.Context, addr string) error {
	if s.Run == nil {
		return errors.New("processor service requires a run function")
	}
	if s.PollInterval < 100*time.Millisecond {
		return errors.New("processor poll interval must be at least 100ms")
	}
	server := &http.Server{Addr: addr, Handler: s.Handler()}
	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Run immediately after startup, then use a bounded polling interval. This
	// makes a restarted worker resume raw items without a busy loop.
	if err := s.runOnce(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(s.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return server.Shutdown(shutdownCtx)
		case err := <-serverErr:
			return err
		case <-ticker.C:
			_ = s.runOnce(ctx) // A later poll recovers transient service/CLI failures.
		}
	}
}

func (s *Service) runOnce(ctx context.Context) error {
	err := s.Run(ctx)
	s.mu.Lock()
	s.ready = err == nil
	s.mu.Unlock()
	return err
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		if s.Monitor == nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Monitor.Snapshot())
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.RLock()
		ready := s.ready
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	return mux
}
