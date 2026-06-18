package route

import (
	"log"
	"sync"
	"time"
)

// TTLChecker runs periodic cleanup of expired routes
type TTLChecker struct {
	store    *Store
	interval time.Duration
	onExpire func([]Route) // Callback for expired routes
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewTTLChecker creates a new TTL checker
func NewTTLChecker(store *Store, interval time.Duration, onExpire func([]Route)) *TTLChecker {
	return &TTLChecker{
		store:    store,
		interval: interval,
		onExpire: onExpire,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start begins the cleanup goroutine
func (t *TTLChecker) Start() {
	go func() {
		ticker := time.NewTicker(t.interval)
		defer ticker.Stop()
		defer close(t.doneCh)

		for {
			select {
			case <-ticker.C:
				expired := t.store.DeleteExpired(time.Now())
				if len(expired) > 0 && t.onExpire != nil {
					log.Printf("TTL: expired %d route(s)", len(expired))
					t.onExpire(expired)
				}
			case <-t.stopCh:
				log.Println("TTL: checker stopping")
				return
			}
		}
	}()
}

// Stop gracefully shuts down the checker
func (t *TTLChecker) Stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
	<-t.doneCh
}
