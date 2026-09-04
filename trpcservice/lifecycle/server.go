// Package lifecycle provides cancellation-aware process lifecycle primitives.
package lifecycle

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Service tracks active work and coordinates a single graceful shutdown.
// Work must be registered before it starts; Shutdown prevents new work.
type Service struct {
	mu       sync.Mutex
	closing  bool
	done     chan struct{}
	wg       sync.WaitGroup
	shutdown sync.Once
	cancel   sync.Once
}

func New() *Service { return &Service{done: make(chan struct{})} }

// Acquire reserves one active-work slot. The returned release function is
// idempotent and should be deferred by the caller.
func (s *Service) Acquire() (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return func() {}, false
	}
	s.wg.Add(1)
	var once sync.Once
	return func() { once.Do(s.wg.Done) }, true
}

// Shutdown stops new work and waits for active work until ctx expires.
// Repeated calls are safe; each call observes the same completion state.
func (s *Service) Shutdown(ctx context.Context) error {
	s.BeginShutdown()
	s.cancel.Do(func() {
		s.mu.Lock()
		close(s.done)
		s.mu.Unlock()
	})
	return s.Wait(ctx)
}

// BeginShutdown stops new work without waiting for active work.
func (s *Service) BeginShutdown() {
	s.shutdown.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
	})
}

// Wait waits for all already-registered work to finish until ctx expires.
func (s *Service) Wait(ctx context.Context) error {
	finished := make(chan struct{})
	go func() { s.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Done() <-chan struct{} { return s.done }

func (s *Service) IsClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// ShutdownWithTimeout is a convenience for process signal handlers.
func (s *Service) ShutdownWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.Shutdown(ctx)
}

var ErrClosing = errors.New("service is shutting down")
