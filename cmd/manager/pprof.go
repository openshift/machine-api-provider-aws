package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

type pprofServer struct {
	Addr string
}

func (s *pprofServer) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.Addr,
		Handler: http.DefaultServeMux,
	}
	klog.Infof("Starting pprof server on %s", s.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("pprof server failed: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func (s *pprofServer) NeedLeaderElection() bool {
	return false
}
