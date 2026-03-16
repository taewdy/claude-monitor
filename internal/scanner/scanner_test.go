package scanner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

func TestScanner_New(t *testing.T) {
	s := New()
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.claude == nil {
		t.Fatal("New() did not initialize claude scanner")
	}
}

func TestScanner_Scan(t *testing.T) {
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	staleTimestamp := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)

	tests := map[string]struct {
		buildFS      func(t *testing.T, homeDir string)
		sessionCount int
	}{
		"returns_sessions_from_claude_scanner": {
			buildFS: func(t *testing.T, homeDir string) {
				// Session files + JSONL files (scanner is JSONL-first).
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: "s1",
					Cwd:       "/tmp/proj1",
					StartedAt: startedAt.UnixMilli(),
				})
				writeConversationJSONL(t, homeDir, encodeCwd("/tmp/proj1"), "s1", []jsonlMessage{
					mkMsg("user", staleTimestamp, nil),
				})
				writeSessionFile(t, homeDir, "999999998.json", sessionFile{
					PID:       999999998,
					SessionID: "s2",
					Cwd:       "/tmp/proj2",
					StartedAt: startedAt.UnixMilli(),
				})
				writeConversationJSONL(t, homeDir, encodeCwd("/tmp/proj2"), "s2", []jsonlMessage{
					mkMsg("user", staleTimestamp, nil),
				})
			},
			sessionCount: 2,
		},
		"empty_when_no_sessions": {
			sessionCount: 0,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			s := &Scanner{
				claude: newClaudeScanner(homeDir),
			}

			got, err := s.Scan(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != tt.sessionCount {
				t.Fatalf("expected %d sessions, got %d: %+v", tt.sessionCount, len(got), got)
			}

			// Every session from the aggregator must have ProviderClaude
			// (only Claude scanner exists currently).
			for i, sess := range got {
				if sess.Provider != model.ProviderClaude {
					t.Errorf("session[%d].Provider = %q, want %q", i, sess.Provider, model.ProviderClaude)
				}
			}
		})
	}
}

// TestScanner_Scan_SortedByLastActiveDesc verifies that Scanner.Scan returns
// sessions sorted by LastActive descending (most recent first).
func TestScanner_Scan_SortedByLastActiveDesc(t *testing.T) {
	homeDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-1 * time.Hour)

	// Create 3 sessions with different LastActive times via JSONL timestamps.
	cwds := []string{"/proj/oldest", "/proj/middle", "/proj/newest"}
	sids := []string{"sess-old", "sess-mid", "sess-new"}
	lastActives := []time.Duration{-10 * time.Minute, -5 * time.Minute, -1 * time.Minute}

	for i := range sids {
		writeSessionFile(t, homeDir, fmt.Sprintf("%d.json", 999999999-i), sessionFile{
			PID:       999999999 - i,
			SessionID: sids[i],
			Cwd:       cwds[i],
			StartedAt: startedAt.UnixMilli(),
		})
		encoded := encodeCwd(cwds[i])
		writeConversationJSONL(t, homeDir, encoded, sids[i], []jsonlMessage{
			mkMsg("user", now.Add(lastActives[i]), nil),
		})
	}

	s := &Scanner{claude: newClaudeScanner(homeDir)}
	got, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(got))
	}

	// Verify descending order by LastActive.
	for i := 1; i < len(got); i++ {
		if got[i].LastActive.After(got[i-1].LastActive) {
			t.Errorf("sessions not sorted by LastActive desc: session[%d].LastActive=%v is after session[%d].LastActive=%v",
				i, got[i].LastActive, i-1, got[i-1].LastActive)
		}
	}

	// The newest session should be first.
	if got[0].ID != "sess-new" {
		t.Errorf("expected first session to be sess-new, got %q", got[0].ID)
	}
}

// TestScanner_Scan_ConcurrencySafe verifies that the goroutine+WaitGroup pattern
// in Scanner.Scan doesn't race. Run with -race flag.
func TestScanner_Scan_ConcurrencySafe(t *testing.T) {
	homeDir := t.TempDir()
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	staleTimestamp := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)

	for i := range 5 {
		sid := fmt.Sprintf("sess-race-%d", i)
		cwd := "/tmp"
		writeSessionFile(t, homeDir, fmt.Sprintf("%d.json", 999999990+i), sessionFile{
			PID:       999999990 + i,
			SessionID: sid,
			Cwd:       cwd,
			StartedAt: startedAt.UnixMilli(),
		})
		writeConversationJSONL(t, homeDir, encodeCwd(cwd), sid, []jsonlMessage{
			mkMsg("user", staleTimestamp, nil),
		})
	}

	s := &Scanner{claude: newClaudeScanner(homeDir)}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Scan(context.Background())
		}()
	}
	wg.Wait()
}
