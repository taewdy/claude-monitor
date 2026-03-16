package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"
	"github.com/universe/claude-monitor/internal/notify"
)

// mockScanner is a test double for SessionScanner.
type mockScanner struct {
	mu       sync.Mutex
	results  []model.SessionInfo
	err      error
	scanFunc func() ([]model.SessionInfo, error)
}

func (m *mockScanner) Scan(_ context.Context) ([]model.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scanFunc != nil {
		return m.scanFunc()
	}
	return m.results, m.err
}

func (m *mockScanner) set(results []model.SessionInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results = results
	m.err = err
}

// mockNotifier records all notifications.
type mockNotifier struct {
	mu      sync.Mutex
	changes []notify.StatusChange
}

func (m *mockNotifier) Notify(_ context.Context, change notify.StatusChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changes = append(m.changes, change)
	return nil
}

func (m *mockNotifier) getChanges() []notify.StatusChange {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]notify.StatusChange, len(m.changes))
	copy(result, m.changes)
	return result
}

func session(id string, status model.Status) model.SessionInfo {
	return model.SessionInfo{
		ID:       id,
		Provider: model.ProviderClaude,
		Status:   status,
		Title:    "test-" + id,
	}
}

func TestNewSessionsNotify(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
		session("s2", model.StatusIdle),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	m.poll(context.Background())

	changes := n.getChanges()
	if len(changes) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(changes))
	}
	for _, c := range changes {
		if c.OldStatus != "" {
			t.Errorf("new session should have empty OldStatus, got %q", c.OldStatus)
		}
	}
}

func TestStatusChangeNotifies(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	// Initial poll — new session.
	m.poll(context.Background())

	// First poll with new status — pending, no notification yet.
	sc.set([]model.SessionInfo{session("s1", model.StatusIdle)}, nil)
	m.poll(context.Background())

	changes := n.getChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 notification (initial only, change pending), got %d", len(changes))
	}

	// Second poll with same new status — confirmed, should notify.
	m.poll(context.Background())

	changes = n.getChanges()
	if len(changes) != 2 {
		t.Fatalf("expected 2 notifications (initial + confirmed change), got %d", len(changes))
	}
	c := changes[1]
	if c.OldStatus != model.StatusActive {
		t.Errorf("expected OldStatus=active, got %q", c.OldStatus)
	}
	if c.NewStatus != model.StatusIdle {
		t.Errorf("expected NewStatus=idle, got %q", c.NewStatus)
	}
}

func TestUnchangedSessionsNoNotification(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	m.poll(context.Background())
	m.poll(context.Background()) // same data

	changes := n.getChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 notification (initial only), got %d", len(changes))
	}
}

func TestDisappearedSessionNotifiesFinished(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	m.poll(context.Background())

	// Session disappears.
	sc.set([]model.SessionInfo{}, nil)
	m.poll(context.Background())

	changes := n.getChanges()
	if len(changes) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(changes))
	}
	c := changes[1]
	if c.Session.ID != "s1" {
		t.Errorf("expected disappeared session ID=s1, got %q", c.Session.ID)
	}
	if c.OldStatus != model.StatusActive {
		t.Errorf("expected OldStatus=active, got %q", c.OldStatus)
	}
	if c.NewStatus != model.StatusFinished {
		t.Errorf("expected NewStatus=finished, got %q", c.NewStatus)
	}
}

func TestScanReturnsCachedResults(t *testing.T) {
	sessions := []model.SessionInfo{
		session("s1", model.StatusActive),
		session("s2", model.StatusIdle),
	}
	sc := &mockScanner{results: sessions}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	m.poll(context.Background())

	result, err := m.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 cached sessions, got %d", len(result))
	}
	if result[0].ID != "s1" || result[1].ID != "s2" {
		t.Errorf("unexpected session IDs: %v", result)
	}
}

func TestScanErrorPreservesCache(t *testing.T) {
	sessions := []model.SessionInfo{
		session("s1", model.StatusActive),
	}
	sc := &mockScanner{results: sessions}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	// Initial successful poll.
	m.poll(context.Background())

	// Scanner returns error.
	sc.set(nil, errors.New("scan failed"))
	m.poll(context.Background())

	// Cache should still have original data.
	result, err := m.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 cached session after error, got %d", len(result))
	}
	if result[0].ID != "s1" {
		t.Errorf("expected cached session s1, got %q", result[0].ID)
	}

	// Prev state should also be preserved — no extra notifications.
	changes := n.getChanges()
	if len(changes) != 1 {
		t.Errorf("expected 1 notification (initial only), got %d", len(changes))
	}
}

func TestScanErrorPreservesPrevState(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	m.poll(context.Background())

	// Error poll.
	sc.set(nil, errors.New("fail"))
	m.poll(context.Background())

	// Recovery with same session — should NOT re-notify since prev is preserved.
	sc.set([]model.SessionInfo{session("s1", model.StatusActive)}, nil)
	m.poll(context.Background())

	changes := n.getChanges()
	if len(changes) != 1 {
		t.Errorf("expected 1 notification total (prev state preserved), got %d", len(changes))
	}
}

func TestStartAndStop(t *testing.T) {
	var callCount atomic.Int32
	sc := &mockScanner{
		scanFunc: func() ([]model.SessionInfo, error) {
			callCount.Add(1)
			return nil, nil
		},
	}
	n := &mockNotifier{}
	m := New(sc, n, 10*time.Millisecond)

	ctx := context.Background()
	m.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	m.Stop()

	// At least the initial poll + a few ticker polls.
	if c := callCount.Load(); c < 2 {
		t.Errorf("expected at least 2 scan calls, got %d", c)
	}
}

func TestStartInitialScanSuppressesNotifications(t *testing.T) {
	sessions := []model.SessionInfo{
		session("s1", model.StatusActive),
		session("s2", model.StatusIdle),
	}
	var callCount atomic.Int32
	sc := &mockScanner{
		scanFunc: func() ([]model.SessionInfo, error) {
			callCount.Add(1)
			return sessions, nil
		},
	}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	ctx := context.Background()
	m.Start(ctx)

	// Initial scan should have populated the cache without notifying.
	if c := callCount.Load(); c != 1 {
		t.Fatalf("expected 1 scan call after Start, got %d", c)
	}
	if changes := n.getChanges(); len(changes) != 0 {
		t.Fatalf("expected 0 notifications after initial scan, got %d", len(changes))
	}

	// Cache should be populated so Scan() returns results immediately.
	result, err := m.Scan(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 cached sessions, got %d", len(result))
	}

	// A subsequent poll that changes status should pend (debounce).
	sc.mu.Lock()
	sc.scanFunc = func() ([]model.SessionInfo, error) {
		callCount.Add(1)
		return []model.SessionInfo{
			session("s1", model.StatusIdle),
			session("s2", model.StatusIdle),
		}, nil
	}
	sc.mu.Unlock()

	m.poll(ctx) // first poll with new status — pending
	if changes := n.getChanges(); len(changes) != 0 {
		t.Fatalf("expected 0 notifications after first changed poll, got %d", len(changes))
	}

	m.poll(ctx) // second poll confirms — should notify
	changes := n.getChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 notification after confirmed status change, got %d", len(changes))
	}
	if changes[0].Session.ID != "s1" {
		t.Errorf("expected notification for s1, got %q", changes[0].Session.ID)
	}
	if changes[0].OldStatus != model.StatusActive || changes[0].NewStatus != model.StatusIdle {
		t.Errorf("unexpected status change: %q -> %q", changes[0].OldStatus, changes[0].NewStatus)
	}

	m.Stop()
}

func TestDebounceCancelledWhenStatusReverts(t *testing.T) {
	sc := &mockScanner{results: []model.SessionInfo{
		session("s1", model.StatusActive),
	}}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	// Initial poll.
	m.poll(context.Background())

	// Status flips to idle — pending.
	sc.set([]model.SessionInfo{session("s1", model.StatusIdle)}, nil)
	m.poll(context.Background())

	// Status flips back to active before confirmation — pending cancelled.
	sc.set([]model.SessionInfo{session("s1", model.StatusActive)}, nil)
	m.poll(context.Background())

	// No status change notification should have fired (only the initial new session).
	changes := n.getChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 notification (initial only), got %d", len(changes))
	}
}

func TestStartContextCancellation(t *testing.T) {
	sc := &mockScanner{results: nil}
	n := &mockNotifier{}
	m := New(sc, n, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	cancel()

	// done channel should close after context cancellation.
	select {
	case <-m.done:
	case <-time.After(time.Second):
		t.Fatal("goroutine did not stop after context cancellation")
	}
}