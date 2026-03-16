// Package monitor polls for session changes and sends notifications.
package monitor

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/universe/claude-monitor/internal/model"
	"github.com/universe/claude-monitor/internal/notify"
	"github.com/universe/claude-monitor/internal/server"
)

// SessionScanner discovers active coding sessions.
type SessionScanner interface {
	Scan(ctx context.Context) ([]model.SessionInfo, error)
}

// Compile-time check: Monitor implements server.SessionScanner.
var _ server.SessionScanner = (*Monitor)(nil)

// Monitor periodically scans for sessions and notifies on status changes.
type Monitor struct {
	scanner  SessionScanner
	notifier notify.Notifier
	interval time.Duration

	mu       sync.RWMutex
	sessions []model.SessionInfo
	prev     map[string]model.Status
	pending  map[string]model.Status // pending status awaiting confirmation

	started bool
	stop    chan struct{}
	done    chan struct{}
}

// New creates a new Monitor.
func New(scanner SessionScanner, notifier notify.Notifier, interval time.Duration) *Monitor {
	return &Monitor{
		scanner:  scanner,
		notifier: notifier,
		interval: interval,
		prev:     make(map[string]model.Status),
		pending:  make(map[string]model.Status),
	}
}

// Start performs an initial scan and launches a background polling goroutine.
// Start must not be called more than once without an intervening Stop.
func (m *Monitor) Start(ctx context.Context) {
	if m.started {
		panic("monitor: Start called without Stop")
	}
	m.started = true
	m.stop = make(chan struct{})
	m.done = make(chan struct{})

	m.seed(ctx)

	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.poll(ctx)
			}
		}
	}()
}

// Stop signals the background goroutine to stop and waits for completion.
func (m *Monitor) Stop() {
	close(m.stop)
	<-m.done
	m.started = false
}

// Scan returns the cached session results. It implements server.SessionScanner.
func (m *Monitor) Scan(_ context.Context) ([]model.SessionInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]model.SessionInfo, len(m.sessions))
	copy(result, m.sessions)
	return result, nil
}

// seed performs a scan that populates the session cache and prev map
// without sending any notifications. Used for the initial scan on startup.
func (m *Monitor) seed(ctx context.Context) {
	sessions, err := m.scanner.Scan(ctx)
	if err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := make(map[string]model.Status, len(sessions))
	for _, s := range sessions {
		current[s.ID] = s.Status
	}

	m.sessions = sessions
	m.prev = current
}

func (m *Monitor) poll(ctx context.Context) {
	sessions, err := m.scanner.Scan(ctx)
	if err != nil {
		// Scan errors must NOT clear cache or prev state.
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := make(map[string]model.Status, len(sessions))
	for _, s := range sessions {
		current[s.ID] = s.Status
	}

	// Detect new and changed sessions with debouncing.
	// A status change must be seen on 2 consecutive polls before notifying.
	// This prevents spurious notifications from CPU fluctuations.
	for _, s := range sessions {
		old, existed := m.prev[s.ID]
		if !existed {
			// New sessions notify immediately (no debounce).
			m.sendNotify(ctx, notify.StatusChange{
				Session:   s,
				OldStatus: "",
				NewStatus: s.Status,
			})
		} else if old != s.Status {
			// Status changed — check if this matches a pending change.
			if pend, ok := m.pending[s.ID]; ok && pend == s.Status {
				// Confirmed: same new status seen twice in a row.
				m.sendNotify(ctx, notify.StatusChange{
					Session:   s,
					OldStatus: old,
					NewStatus: s.Status,
				})
				delete(m.pending, s.ID)
			} else {
				// First time seeing this new status — record as pending.
				m.pending[s.ID] = s.Status
			}
		} else {
			// Status unchanged — clear any pending transition.
			delete(m.pending, s.ID)
		}
	}

	// Detect disappeared sessions (notify immediately).
	for id, oldStatus := range m.prev {
		if _, exists := current[id]; !exists {
			m.sendNotify(ctx, notify.StatusChange{
				Session:   model.SessionInfo{ID: id},
				OldStatus: oldStatus,
				NewStatus: model.StatusFinished,
			})
			delete(m.pending, id)
		}
	}

	m.sessions = sessions
	// Only update prev for confirmed transitions and unchanged statuses.
	// Keep prev[id] at the old value while a change is still pending,
	// so the next poll can confirm against the same baseline.
	for id, status := range current {
		if _, isPending := m.pending[id]; !isPending {
			m.prev[id] = status
		}
	}
	// Remove prev entries for disappeared sessions.
	for id := range m.prev {
		if _, exists := current[id]; !exists {
			delete(m.prev, id)
		}
	}
}

func (m *Monitor) sendNotify(ctx context.Context, change notify.StatusChange) {
	if err := m.notifier.Notify(ctx, change); err != nil {
		log.Printf("monitor: notification error: %v", err)
	}
}
