package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

// ---------- test types ----------

// workspaceYAML represents the fields in workspace.yaml that the scanner must parse.
type workspaceYAML struct {
	ID        string
	Cwd       string
	GitBranch string
	GitRemote string
	CreatedAt time.Time
	UpdatedAt time.Time
	Summary   string
}

// copilotEvent represents a single line in events.jsonl.
type copilotEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

// ideLockFile represents the content of an IDE lock file.
type ideLockFile struct {
	PID     int    `json:"pid"`
	IDEName string `json:"ideName"`
}

// ---------- test helpers ----------

// writeWorkspaceYAML writes a simple workspace.yaml file for a copilot session.
// We write the YAML manually to avoid a yaml dependency in tests.
func writeWorkspaceYAML(t *testing.T, homeDir, sessionID string, ws workspaceYAML) {
	t.Helper()
	dir := filepath.Join(homeDir, ".copilot", "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`id: %s
cwd: %s
branch: %s
repository: %s
created_at: %s
updated_at: %s
summary: %s
`,
		ws.ID,
		ws.Cwd,
		ws.GitBranch,
		ws.GitRemote,
		ws.CreatedAt.Format(time.RFC3339),
		ws.UpdatedAt.Format(time.RFC3339),
		ws.Summary,
	)
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEventsJSONL writes an events.jsonl file for a copilot session.
func writeEventsJSONL(t *testing.T, homeDir, sessionID string, events []copilotEvent) {
	t.Helper()
	dir := filepath.Join(homeDir, ".copilot", "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

// writeIDELockFile writes an IDE lock file for a copilot session.
func writeIDELockFile(t *testing.T, homeDir, sessionID string, lock ideLockFile) {
	t.Helper()
	dir := filepath.Join(homeDir, ".copilot", "ide")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".lock"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSessionLockFile writes an inuse.{PID}.lock file inside the session directory
// (used by the Copilot CLI for liveness detection).
func writeSessionLockFile(t *testing.T, homeDir, sessionID string, pid int) {
	t.Helper()
	dir := filepath.Join(homeDir, ".copilot", "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockName := fmt.Sprintf("inuse.%d.lock", pid)
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte(fmt.Sprintf("%d", pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// createCopilotSessionDir creates a minimal session directory (just the UUID dir, no files).
func createCopilotSessionDir(t *testing.T, homeDir, sessionID string) {
	t.Helper()
	dir := filepath.Join(homeDir, ".copilot", "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// assertCopilotSession compares key fields of a SessionInfo against expected values.
func assertCopilotSession(t *testing.T, idx int, got, want model.SessionInfo) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("session[%d].ID = %q, want %q", idx, got.ID, want.ID)
	}
	if got.Provider != want.Provider {
		t.Errorf("session[%d].Provider = %q, want %q", idx, got.Provider, want.Provider)
	}
	if got.Status != want.Status {
		t.Errorf("session[%d].Status = %q, want %q", idx, got.Status, want.Status)
	}
	if got.Title != want.Title {
		t.Errorf("session[%d].Title = %q, want %q", idx, got.Title, want.Title)
	}
	if got.ProjectDir != want.ProjectDir {
		t.Errorf("session[%d].ProjectDir = %q, want %q", idx, got.ProjectDir, want.ProjectDir)
	}
	if got.GitBranch != want.GitBranch {
		t.Errorf("session[%d].GitBranch = %q, want %q", idx, got.GitBranch, want.GitBranch)
	}
}

// ---------- scan() tests ----------

func TestCopilotScanner_Scan(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	tests := map[string]struct {
		buildFS   func(t *testing.T, homeDir string)
		sessions  []model.SessionInfo // nil means check count only
		count     int
		cancelCtx bool
		wantErr   bool
	}{
		"no_copilot_directory": {
			// ~/.copilot doesn't exist → return empty slice, no error.
			count: 0,
		},
		"empty_session_state_directory": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".copilot", "session-state")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			count: 0,
		},
		"single_active_session": {
			// No shutdown event + last event < 60s ago → active.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "550e8400-e29b-41d4-a716-446655440000"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID:        sid,
					Cwd:       "/home/user/project",
					GitBranch: "feat/copilot",
					GitRemote: "origin",
					CreatedAt: now.Add(-1 * time.Hour),
					UpdatedAt: now.Add(-5 * time.Second),
					Summary:   "Implementing auth",
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Second).Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "550e8400-e29b-41d4-a716-446655440000",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusActive,
					Title:      "Implementing auth",
					ProjectDir: "/home/user/project",
					GitBranch:  "feat/copilot",
				},
			},
		},
		"single_finished_stale_session": {
			// No shutdown event + last event > 2min ago + no lock → finished.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "660e8400-e29b-41d4-a716-446655440001"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID:        sid,
					Cwd:       "/home/user/backend",
					GitBranch: "main",
					CreatedAt: now.Add(-2 * time.Hour),
					UpdatedAt: now.Add(-5 * time.Minute),
					Summary:   "DB refactor",
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "660e8400-e29b-41d4-a716-446655440001",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					Title:      "DB refactor",
					ProjectDir: "/home/user/backend",
					GitBranch:  "main",
				},
			},
		},
		"single_waiting_session": {
			// Last event is assistant.turn_end → waiting.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "770e8400-e29b-41d4-a716-446655440002"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID:        sid,
					Cwd:       "/home/user/frontend",
					GitBranch: "feat/ui",
					CreatedAt: now.Add(-30 * time.Minute),
					UpdatedAt: now.Add(-10 * time.Second),
					Summary:   "UI work",
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "user.message", Timestamp: now.Add(-30 * time.Second).Unix()},
					{Type: "assistant.turn_end", Timestamp: now.Add(-10 * time.Second).Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "770e8400-e29b-41d4-a716-446655440002",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusWaiting,
					Title:      "UI work",
					ProjectDir: "/home/user/frontend",
					GitBranch:  "feat/ui",
				},
			},
		},
		"single_finished_session": {
			// Has session.shutdown event → finished.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "880e8400-e29b-41d4-a716-446655440003"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID:        sid,
					Cwd:       "/home/user/legacy",
					GitBranch: "main",
					CreatedAt: now.Add(-3 * time.Hour),
					UpdatedAt: now.Add(-2 * time.Hour),
					Summary:   "Done task",
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "user.message", Timestamp: now.Add(-3 * time.Hour).Unix()},
					{Type: "assistant.message", Timestamp: now.Add(-2*time.Hour - 30*time.Minute).Unix()},
					{Type: "session.shutdown", Timestamp: now.Add(-2 * time.Hour).Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "880e8400-e29b-41d4-a716-446655440003",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					Title:      "Done task",
					ProjectDir: "/home/user/legacy",
					GitBranch:  "main",
				},
			},
		},
		"idle_session_with_ide_lock_stale_event": {
			// IDE lock file exists with live PID but event > 2min ago → idle.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "990e8400-e29b-41d4-a716-446655440004"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID:        sid,
					Cwd:       "/home/user/ide-proj",
					GitBranch: "dev",
					CreatedAt: now.Add(-1 * time.Hour),
					UpdatedAt: now.Add(-3 * time.Minute),
					Summary:   "IDE session",
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-3 * time.Minute).Unix()},
				})
				writeIDELockFile(t, homeDir, sid, ideLockFile{
					PID:     os.Getpid(), // Use current PID so it's alive.
					IDEName: "vscode",
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "990e8400-e29b-41d4-a716-446655440004",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusIdle,
					Title:      "IDE session",
					ProjectDir: "/home/user/ide-proj",
					GitBranch:  "dev",
				},
			},
		},
		"multiple_sessions_with_different_statuses": {
			buildFS: func(t *testing.T, homeDir string) {
				// Active session
				writeWorkspaceYAML(t, homeDir, "uuid-active", workspaceYAML{
					ID: "uuid-active", Cwd: "/proj/a", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-5 * time.Second),
					Summary: "Active",
				})
				writeEventsJSONL(t, homeDir, "uuid-active", []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Second).Unix()},
				})

				// Finished session
				writeWorkspaceYAML(t, homeDir, "uuid-done", workspaceYAML{
					ID: "uuid-done", Cwd: "/proj/b", GitBranch: "main",
					CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour),
					Summary: "Done",
				})
				writeEventsJSONL(t, homeDir, "uuid-done", []copilotEvent{
					{Type: "session.shutdown", Timestamp: now.Add(-4 * time.Hour).Unix()},
				})
			},
			count: 2,
		},
		"session_dir_without_workspace_yaml": {
			// UUID dir exists but no workspace.yaml → skip gracefully.
			buildFS: func(t *testing.T, homeDir string) {
				createCopilotSessionDir(t, homeDir, "no-workspace-uuid")
			},
			count: 0,
		},
		"session_with_malformed_workspace_yaml": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "malformed-yaml-uuid"
				dir := filepath.Join(homeDir, ".copilot", "session-state", sid)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("{{{{not yaml at all::::"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// Should skip gracefully, no panic.
			count: 0,
		},
		"session_without_events_jsonl": {
			// workspace.yaml exists but no events.jsonl and no lock → finished.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "no-events-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-30 * time.Minute),
					Summary: "No events",
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "no-events-uuid",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					Title:      "No events",
					ProjectDir: "/proj",
					GitBranch:  "main",
				},
			},
		},
		"cli_session_no_events_with_alive_lock_recent": {
			// CLI session: no events.jsonl but inuse.*.lock with alive PID,
			// workspace updated recently → active.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "cli-active-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/home/user/cli-proj", GitBranch: "main",
					CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute),
					Summary: "",
				})
				writeSessionLockFile(t, homeDir, sid, os.Getpid())
			},
			sessions: []model.SessionInfo{
				{
					ID:         "cli-active-uuid",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusActive,
					ProjectDir: "/home/user/cli-proj",
					GitBranch:  "main",
				},
			},
		},
		"cli_session_no_events_with_alive_lock_stale": {
			// CLI session: no events.jsonl, inuse.*.lock with alive PID,
			// but workspace updated >10 min ago → idle (lingering daemon).
			buildFS: func(t *testing.T, homeDir string) {
				sid := "cli-stale-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/home/user/cli-proj", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
				})
				writeSessionLockFile(t, homeDir, sid, os.Getpid())
			},
			sessions: []model.SessionInfo{
				{
					ID:         "cli-stale-uuid",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusIdle,
					ProjectDir: "/home/user/cli-proj",
					GitBranch:  "main",
				},
			},
		},
		"cli_session_no_events_with_dead_lock": {
			// CLI session: no events.jsonl and inuse.*.lock with dead PID → finished.
			buildFS: func(t *testing.T, homeDir string) {
				sid := "cli-dead-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/home/user/cli-proj",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
				})
				writeSessionLockFile(t, homeDir, sid, 999999999)
			},
			sessions: []model.SessionInfo{
				{
					ID:         "cli-dead-uuid",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					ProjectDir: "/home/user/cli-proj",
				},
			},
		},
		"malformed_events_jsonl_lines_skipped": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "bad-events-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-10 * time.Second),
					Summary: "Bad events",
				})
				dir := filepath.Join(homeDir, ".copilot", "session-state", sid)
				content := fmt.Sprintf(`{"type":"user.message","timestamp":%d}
{bad json line
{"type":"assistant.message","timestamp":%d}
`, now.Add(-30*time.Second).Unix(), now.Add(-10*time.Second).Unix())
				if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// Should parse valid lines, skip bad ones.
			count: 1,
		},
		"empty_events_jsonl": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "empty-events-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-30 * time.Minute),
					Summary: "Empty events",
				})
				dir := filepath.Join(homeDir, ".copilot", "session-state", sid)
				if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			sessions: []model.SessionInfo{
				{
					ID:         "empty-events-uuid",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					Title:      "Empty events",
					ProjectDir: "/proj",
					GitBranch:  "main",
				},
			},
		},
		"context_cancelled": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "cancel-uuid"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now,
				})
			},
			cancelCtx: true,
			wantErr:   true,
		},
		"empty_summary_and_branch": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "ws-edge-uuid-1"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now,
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "session.shutdown", Timestamp: now.Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "ws-edge-uuid-1",
					Provider:   model.ProviderCopilot,
					Status:     model.StatusFinished,
					Title:      "",
					ProjectDir: "/proj",
					GitBranch:  "",
				},
			},
		},
		"empty_cwd": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "ws-edge-uuid-2"
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now,
				})
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "session.shutdown", Timestamp: now.Unix()},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:       "ws-edge-uuid-2",
					Provider: model.ProviderCopilot,
					Status:   model.StatusFinished,
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			ctx := context.Background()
			if tt.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			cs := newCopilotScanner(homeDir)
			got, err := cs.scan(ctx)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if tt.sessions != nil {
				if len(got) != len(tt.sessions) {
					t.Fatalf("expected %d sessions, got %d: %+v", len(tt.sessions), len(got), got)
				}
				for i, want := range tt.sessions {
					assertCopilotSession(t, i, got[i], want)
				}
				return
			}

			if len(got) != tt.count {
				t.Fatalf("expected %d sessions, got %d: %+v", tt.count, len(got), got)
			}
		})
	}
}

// ---------- Status determination tests ----------

func TestCopilotScanner_StatusDetermination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	tests := map[string]struct {
		buildFS func(t *testing.T, homeDir, sid string)
		status  model.Status
	}{
		"no_shutdown_recent_event_is_active": {
			// Last event < 60s ago, no shutdown → active.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-10 * time.Second).Unix()},
				})
			},
			status: model.StatusActive,
		},
		"no_shutdown_stale_event_no_lock_is_finished": {
			// Last event > 2min ago, no shutdown, no lock → finished.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
			},
			status: model.StatusFinished,
		},
		"last_event_assistant_turn_end_is_waiting": {
			// Last event is assistant.turn_end → waiting.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "user.message", Timestamp: now.Add(-30 * time.Second).Unix()},
					{Type: "assistant.turn_end", Timestamp: now.Add(-15 * time.Second).Unix()},
				})
			},
			status: model.StatusWaiting,
		},
		"shutdown_event_is_finished": {
			// Has session.shutdown → finished.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "user.message", Timestamp: now.Add(-1 * time.Hour).Unix()},
					{Type: "session.shutdown", Timestamp: now.Add(-30 * time.Minute).Unix()},
				})
			},
			status: model.StatusFinished,
		},
		"ide_lock_alive_pid_stale_event_is_idle": {
			// Event is stale (> 2 min) + IDE lock with alive PID → idle.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
				writeIDELockFile(t, homeDir, sid, ideLockFile{
					PID:     os.Getpid(),
					IDEName: "vscode",
				})
			},
			status: model.StatusIdle,
		},
		"ide_lock_dead_pid_stale_event_is_finished": {
			// IDE lock exists but PID is dead + stale event → finished.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
				writeIDELockFile(t, homeDir, sid, ideLockFile{
					PID:     999999999, // Dead PID.
					IDEName: "vscode",
				})
			},
			status: model.StatusFinished,
		},
		"session_lock_alive_pid_stale_event_is_idle": {
			// Stale event (> 2 min) + inuse.*.lock with alive PID → idle.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
				writeSessionLockFile(t, homeDir, sid, os.Getpid())
			},
			status: model.StatusIdle,
		},
		"session_lock_dead_pid_stale_event_is_finished": {
			// inuse.*.lock with dead PID + stale event → finished.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeEventsJSONL(t, homeDir, sid, []copilotEvent{
					{Type: "assistant.message", Timestamp: now.Add(-5 * time.Minute).Unix()},
				})
				writeSessionLockFile(t, homeDir, sid, 999999999)
			},
			status: model.StatusFinished,
		},
		"no_events_session_lock_alive_is_active": {
			// No events at all, but inuse.*.lock with alive PID and recent workspace → active.
			buildFS: func(t *testing.T, homeDir, sid string) {
				writeSessionLockFile(t, homeDir, sid, os.Getpid())
			},
			status: model.StatusActive,
		},
		"no_events_session_lock_alive_stale_workspace_is_idle": {
			// No events, alive PID, but workspace updated >10 min ago → idle.
			buildFS: func(t *testing.T, homeDir, sid string) {
				// Overwrite workspace.yaml with a stale updated_at.
				writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
					ID: sid, Cwd: "/proj", GitBranch: "main",
					CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
					Summary: "Status test",
				})
				writeSessionLockFile(t, homeDir, sid, os.Getpid())
			},
			status: model.StatusIdle,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			sid := "status-test-uuid"

			writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
				ID: sid, Cwd: "/proj", GitBranch: "main",
				CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
				Summary: "Status test",
			})
			tt.buildFS(t, homeDir, sid)

			cs := newCopilotScanner(homeDir)
			got, err := cs.scan(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 session, got %d", len(got))
			}
			if got[0].Status != tt.status {
				t.Errorf("status = %q, want %q", got[0].Status, tt.status)
			}
		})
	}
}

// ---------- Status boundary tests ----------

func TestCopilotScanner_StatusBoundaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	tests := map[string]struct {
		lastEventAge time.Duration
		want         model.Status
	}{
		"just_updated_is_active": {
			lastEventAge: 0,
			want:         model.StatusActive,
		},
		"119_seconds_ago_is_active": {
			lastEventAge: 119 * time.Second,
			want:         model.StatusActive,
		},
		"2_minutes_ago_is_finished": {
			// Boundary: exactly 2min should transition out of active.
			// No alive lock → finished.
			lastEventAge: 2 * time.Minute,
			want:         model.StatusFinished,
		},
		"5_minutes_ago_is_finished": {
			lastEventAge: 5 * time.Minute,
			want:         model.StatusFinished,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			sid := "boundary-uuid"

			writeWorkspaceYAML(t, homeDir, sid, workspaceYAML{
				ID: sid, Cwd: "/proj",
				CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now,
			})
			writeEventsJSONL(t, homeDir, sid, []copilotEvent{
				{Type: "user.message", Timestamp: now.Add(-tt.lastEventAge).Unix()},
			})

			cs := newCopilotScanner(homeDir)
			got, err := cs.scan(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 session, got %d", len(got))
			}
			if got[0].Status != tt.want {
				t.Errorf("status = %q, want %q (lastEventAge: %v)",
					got[0].Status, tt.want, tt.lastEventAge)
			}
		})
	}
}

// ---------- Sorting tests ----------

func TestCopilotScanner_SortedByUpdatedAtDesc(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	homeDir := t.TempDir()

	// Create sessions with different updated_at times.
	sessions := []struct {
		id        string
		updatedAt time.Time
	}{
		{"uuid-oldest", now.Add(-3 * time.Hour)},
		{"uuid-middle", now.Add(-1 * time.Hour)},
		{"uuid-newest", now.Add(-5 * time.Minute)},
	}

	for _, s := range sessions {
		writeWorkspaceYAML(t, homeDir, s.id, workspaceYAML{
			ID: s.id, Cwd: "/proj/" + s.id,
			CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: s.updatedAt,
			Summary: s.id,
		})
		writeEventsJSONL(t, homeDir, s.id, []copilotEvent{
			{Type: "session.shutdown", Timestamp: s.updatedAt.Unix()},
		})
	}

	cs := newCopilotScanner(homeDir)
	got, err := cs.scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(got))
	}

	// Verify descending order by LastActive.
	for i := 1; i < len(got); i++ {
		if got[i].LastActive.After(got[i-1].LastActive) {
			t.Errorf("sessions not sorted by LastActive desc: session[%d].LastActive=%v after session[%d].LastActive=%v",
				i, got[i].LastActive, i-1, got[i-1].LastActive)
		}
	}
}

// ---------- Constructor test ----------

func TestNewCopilotScanner(t *testing.T) {
	cs := newCopilotScanner("/home/testuser")
	if cs == nil {
		t.Fatal("newCopilotScanner returned nil")
	}
	if cs.homeDir != "/home/testuser" {
		t.Errorf("homeDir = %q, want %q", cs.homeDir, "/home/testuser")
	}
}

// ---------- Aggregator wiring test ----------

func TestScanner_IncludesCopilotScanner(t *testing.T) {
	s := New()
	if s.copilot == nil {
		t.Fatal("New() did not initialize copilot scanner")
	}
}

// ---------- Non-UUID directory names ignored ----------

func TestCopilotScanner_NonUUIDDirNamesIgnored(t *testing.T) {
	homeDir := t.TempDir()

	// Create non-UUID named directories that should be skipped.
	for _, name := range []string{"temp", ".hidden", "not-a-uuid", "readme.txt"} {
		dir := filepath.Join(homeDir, ".copilot", "session-state", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cs := newCopilotScanner(homeDir)
	got, err := cs.scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Non-UUID dirs have no workspace.yaml, so they should be skipped.
	if len(got) != 0 {
		t.Fatalf("expected 0 sessions from non-UUID dirs, got %d: %+v", len(got), got)
	}
}
