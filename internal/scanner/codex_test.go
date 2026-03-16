package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"

	_ "modernc.org/sqlite"
)

// ---------- test helpers ----------

// createCodexDB creates a Codex state_5.sqlite database at the expected path
// with the threads table schema and inserts the given rows. If archived is
// provided, it must have the same length as rows; otherwise all rows default
// to archived=0.
func createCodexDB(t *testing.T, homeDir string, rows []threadRow, archived ...[]bool) {
	t.Helper()
	dir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "state_5.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		title TEXT,
		cwd TEXT,
		tokens_used INTEGER,
		updated_at INTEGER,
		git_branch TEXT,
		source TEXT,
		rollout_path TEXT,
		archived INTEGER DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}

	var archFlags []bool
	if len(archived) > 0 {
		archFlags = archived[0]
	}

	for i, r := range rows {
		arch := 0
		if archFlags != nil && archFlags[i] {
			arch = 1
		}
		_, err = db.Exec(
			`INSERT INTO threads (id, title, cwd, tokens_used, updated_at, git_branch, source, rollout_path, archived)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Title, r.Cwd, r.TokensUsed, r.UpdatedAt, r.GitBranch, r.Source, r.RolloutPath, arch,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// assertCodexSession compares the key fields of a SessionInfo against expected values.
func assertCodexSession(t *testing.T, idx int, got, want model.SessionInfo) {
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
	if got.InputTokens != want.InputTokens {
		t.Errorf("session[%d].InputTokens = %d, want %d", idx, got.InputTokens, want.InputTokens)
	}
}

// writeSessionIndexJSONL writes a session_index.jsonl file in the real Codex format.
func writeSessionIndexJSONL(t *testing.T, homeDir string, entries []sessionIndexEntry) {
	t.Helper()
	dir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "session_index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		// Write in the real Codex format: thread_name and ISO updated_at.
		raw := map[string]string{
			"id":          e.ID,
			"thread_name": e.Title,
			"updated_at":  e.UpdatedAt.Format(time.RFC3339Nano),
		}
		if err := enc.Encode(raw); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRolloutFile writes a rollout JSONL file with a session_meta entry and
// optional additional event lines. Sets file mtime to the given time.
func writeRolloutFile(t *testing.T, homeDir, sessionID, cwd, gitBranch string, startedAt, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(homeDir, ".codex", "sessions",
		startedAt.Format("2006"), startedAt.Format("01"), startedAt.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	filename := fmt.Sprintf("rollout-%s-%s.jsonl",
		startedAt.Format("2006-01-02T15-04-05"), sessionID)
	path := filepath.Join(dir, filename)

	meta := map[string]interface{}{
		"type":      "session_meta",
		"timestamp": startedAt.Format(time.RFC3339Nano),
		"payload": map[string]interface{}{
			"id":        sessionID,
			"cwd":       cwd,
			"source":    "cli",
			"timestamp": startedAt.Format(time.RFC3339Nano),
			"git": map[string]string{
				"branch": gitBranch,
			},
		},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------- scan() tests with DB ----------

func TestCodexScanner_ScanFromDB(t *testing.T) {
	now := time.Now().Unix()

	tests := map[string]struct {
		buildFS  func(t *testing.T, homeDir string)
		sessions []model.SessionInfo // nil means check count only
		count    int
		wantErr  bool
	}{
		"no_codex_directory": {
			count: 0,
		},
		"empty_database_no_threads": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, nil)
			},
			count: 0,
		},
		"single_active_thread": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-001",
						Title:      "Fix auth flow",
						Cwd:        "/home/user/project",
						TokensUsed: 5000,
						UpdatedAt:  now - 10,
						GitBranch:  "feat/auth",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:          "thread-001",
					Provider:    model.ProviderCodex,
					Status:      model.StatusActive,
					Title:       "Fix auth flow",
					InputTokens: 5000,
					ProjectDir:  "/home/user/project",
					GitBranch:   "feat/auth",
				},
			},
		},
		"single_idle_thread": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-002",
						Title:      "Refactor DB layer",
						Cwd:        "/home/user/backend",
						TokensUsed: 8000,
						UpdatedAt:  now - 300,
						GitBranch:  "refactor/db",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:          "thread-002",
					Provider:    model.ProviderCodex,
					Status:      model.StatusIdle,
					Title:       "Refactor DB layer",
					InputTokens: 8000,
					ProjectDir:  "/home/user/backend",
					GitBranch:   "refactor/db",
				},
			},
		},
		"single_finished_thread": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-003",
						Title:      "Old task",
						Cwd:        "/home/user/legacy",
						TokensUsed: 12000,
						UpdatedAt:  now - 3600,
						GitBranch:  "main",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:          "thread-003",
					Provider:    model.ProviderCodex,
					Status:      model.StatusFinished,
					Title:       "Old task",
					InputTokens: 12000,
					ProjectDir:  "/home/user/legacy",
					GitBranch:   "main",
				},
			},
		},
		"archived_threads_excluded": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir,
					[]threadRow{
						{ID: "active-thread", Title: "Active", Cwd: "/proj", TokensUsed: 100, UpdatedAt: now - 10},
						{ID: "archived-thread", Title: "Archived", Cwd: "/proj", TokensUsed: 200, UpdatedAt: now - 10},
					},
					[]bool{false, true},
				)
			},
			count: 1,
		},
		"thread_with_empty_fields": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:        "thread-empty",
						Title:     "",
						Cwd:       "",
						UpdatedAt: now - 10,
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:       "thread-empty",
					Provider: model.ProviderCodex,
					Status:   model.StatusActive,
				},
			},
		},
		"corrupt_database_file": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".codex")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("not a database"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			cs := newCodexScanner(homeDir)
			got, err := cs.scan(context.Background())

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
					assertCodexSession(t, i, got[i], want)
				}
				return
			}

			if len(got) != tt.count {
				t.Fatalf("expected %d sessions, got %d: %+v", tt.count, len(got), got)
			}
		})
	}
}

// ---------- Rollout file fallback tests ----------

func TestCodexScanner_ScanFromRolloutFiles(t *testing.T) {
	now := time.Now()

	t.Run("discovers_sessions_from_rollout_files", func(t *testing.T) {
		homeDir := t.TempDir()

		// Create rollout files with different mtimes.
		writeRolloutFile(t, homeDir, "sess-recent", "/proj/a", "main",
			now.Add(-1*time.Hour), now.Add(-30*time.Second))
		writeRolloutFile(t, homeDir, "sess-old", "/proj/b", "dev",
			now.Add(-2*time.Hour), now.Add(-1*time.Hour))

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d: %+v", len(got), got)
		}
		// Most recent first.
		if got[0].ID != "sess-recent" {
			t.Errorf("first session ID = %q, want %q", got[0].ID, "sess-recent")
		}
		if got[0].ProjectDir != "/proj/a" {
			t.Errorf("ProjectDir = %q, want %q", got[0].ProjectDir, "/proj/a")
		}
		if got[0].GitBranch != "main" {
			t.Errorf("GitBranch = %q, want %q", got[0].GitBranch, "main")
		}
	})

	t.Run("rollout_with_session_index_title", func(t *testing.T) {
		homeDir := t.TempDir()

		writeRolloutFile(t, homeDir, "sess-titled", "/proj", "main",
			now.Add(-1*time.Hour), now.Add(-5*time.Minute))
		writeSessionIndexJSONL(t, homeDir, []sessionIndexEntry{
			{ID: "sess-titled", Title: "My Task", UpdatedAt: now.Add(-5 * time.Minute)},
		})

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 session, got %d", len(got))
		}
		if got[0].Title != "My Task" {
			t.Errorf("Title = %q, want %q", got[0].Title, "My Task")
		}
	})

	t.Run("status_from_file_mtime", func(t *testing.T) {
		homeDir := t.TempDir()

		// Active: mtime < 60s
		writeRolloutFile(t, homeDir, "s-active", "/proj", "",
			now.Add(-1*time.Hour), now.Add(-10*time.Second))
		// Idle: mtime 60s-10min
		writeRolloutFile(t, homeDir, "s-idle", "/proj", "",
			now.Add(-2*time.Hour), now.Add(-5*time.Minute))
		// Finished: mtime > 10min
		writeRolloutFile(t, homeDir, "s-done", "/proj", "",
			now.Add(-3*time.Hour), now.Add(-1*time.Hour))

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 sessions, got %d", len(got))
		}

		statusByID := map[string]model.Status{}
		for _, s := range got {
			statusByID[s.ID] = s.Status
		}
		if statusByID["s-active"] != model.StatusActive {
			t.Errorf("s-active status = %q, want active", statusByID["s-active"])
		}
		if statusByID["s-idle"] != model.StatusIdle {
			t.Errorf("s-idle status = %q, want idle", statusByID["s-idle"])
		}
		if statusByID["s-done"] != model.StatusFinished {
			t.Errorf("s-done status = %q, want finished", statusByID["s-done"])
		}
	})

	t.Run("empty_db_falls_through_to_rollout", func(t *testing.T) {
		homeDir := t.TempDir()

		// Create empty DB AND rollout files — should use rollout fallback.
		createCodexDB(t, homeDir, nil)
		writeRolloutFile(t, homeDir, "fallback-sess", "/proj", "main",
			now.Add(-1*time.Hour), now.Add(-30*time.Second))

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 session, got %d", len(got))
		}
		if got[0].ID != "fallback-sess" {
			t.Errorf("ID = %q, want %q", got[0].ID, "fallback-sess")
		}
	})

	t.Run("no_sessions_dir_returns_empty", func(t *testing.T) {
		homeDir := t.TempDir()
		// Create .codex with empty DB but no sessions dir.
		createCodexDB(t, homeDir, nil)

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected 0 sessions, got %d", len(got))
		}
	})

	t.Run("limits_to_50_sessions", func(t *testing.T) {
		homeDir := t.TempDir()
		for i := 0; i < 60; i++ {
			sid := fmt.Sprintf("sess-%03d", i)
			writeRolloutFile(t, homeDir, sid, "/proj", "",
				now.Add(-time.Duration(i)*time.Hour),
				now.Add(-time.Duration(i)*time.Minute))
		}

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 50 {
			t.Fatalf("expected 50 sessions (limit), got %d", len(got))
		}
	})

	t.Run("context_cancellation", func(t *testing.T) {
		homeDir := t.TempDir()
		writeRolloutFile(t, homeDir, "sess-cancel", "/proj", "",
			now.Add(-1*time.Hour), now)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		cs := newCodexScanner(homeDir)
		_, err := cs.scan(ctx)
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
	})
}

// ---------- Status boundary tests ----------

func TestCodexScanner_StatusBoundaries(t *testing.T) {
	now := time.Now().Unix()

	tests := map[string]struct {
		updatedAt int64
		want      model.Status
	}{
		"just_updated_is_active": {
			updatedAt: now,
			want:      model.StatusActive,
		},
		"59_seconds_ago_is_active": {
			updatedAt: now - 59,
			want:      model.StatusActive,
		},
		"60_seconds_ago_is_idle": {
			updatedAt: now - 60,
			want:      model.StatusIdle,
		},
		"5_minutes_ago_is_idle": {
			updatedAt: now - 300,
			want:      model.StatusIdle,
		},
		"599_seconds_ago_is_idle": {
			updatedAt: now - 599,
			want:      model.StatusIdle,
		},
		"600_seconds_ago_is_finished": {
			updatedAt: now - 600,
			want:      model.StatusFinished,
		},
		"1_hour_ago_is_finished": {
			updatedAt: now - 3600,
			want:      model.StatusFinished,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			createCodexDB(t, homeDir, []threadRow{
				{ID: "boundary-test", Cwd: "/proj", UpdatedAt: tt.updatedAt},
			})

			cs := newCodexScanner(homeDir)
			got, err := cs.scan(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 session, got %d", len(got))
			}
			if got[0].Status != tt.want {
				t.Errorf("status = %q, want %q (updatedAt offset: %ds)",
					got[0].Status, tt.want, now-tt.updatedAt)
			}
		})
	}
}

// ---------- openDB tests ----------

func TestCodexScanner_OpenDB(t *testing.T) {
	tests := map[string]struct {
		buildFS func(t *testing.T, homeDir string)
		wantErr bool
	}{
		"missing_codex_dir": {
			wantErr: true,
		},
		"missing_db_file": {
			buildFS: func(t *testing.T, homeDir string) {
				if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		"valid_db": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, nil)
			},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			cs := newCodexScanner(homeDir)
			db, err := cs.openDB()

			if tt.wantErr {
				if err == nil && db != nil {
					_, queryErr := db.Exec("SELECT 1")
					if queryErr == nil {
						t.Error("expected error opening DB, but query succeeded")
					}
					db.Close()
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if db == nil {
				t.Fatal("expected non-nil db")
			}
			defer db.Close()

			var n int
			if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
				t.Fatalf("DB not queryable: %v", err)
			}
		})
	}
}

// ---------- queryThreads tests ----------

func TestCodexScanner_QueryThreads(t *testing.T) {
	now := time.Now().Unix()

	tests := map[string]struct {
		rows []threadRow
		want int
	}{
		"empty_table": {
			rows: nil,
			want: 0,
		},
		"single_row": {
			rows: []threadRow{
				{ID: "t1", Title: "Task 1", Cwd: "/proj", TokensUsed: 500, UpdatedAt: now, GitBranch: "main", Source: "cli"},
			},
			want: 1,
		},
		"multiple_rows_ordered_by_updated_at_desc": {
			rows: []threadRow{
				{ID: "t-old", UpdatedAt: now - 3600},
				{ID: "t-mid", UpdatedAt: now - 600},
				{ID: "t-new", UpdatedAt: now - 10},
			},
			want: 3,
		},
		"max_50_rows": {
			rows: func() []threadRow {
				rows := make([]threadRow, 60)
				for i := range rows {
					rows[i] = threadRow{
						ID:        fmt.Sprintf("t-%03d", i),
						UpdatedAt: now - int64(i),
					}
				}
				return rows
			}(),
			want: 50,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			createCodexDB(t, homeDir, tt.rows)

			dbPath := filepath.Join(homeDir, ".codex", "state_5.sqlite")
			db, err := sql.Open("sqlite", dbPath+"?mode=ro")
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer db.Close()

			cs := newCodexScanner(homeDir)
			got, err := cs.queryThreads(context.Background(), db)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("expected %d rows, got %d", tt.want, len(got))
			}

			if len(got) > 1 {
				for i := 1; i < len(got); i++ {
					if got[i].UpdatedAt > got[i-1].UpdatedAt {
						t.Errorf("rows not sorted by updated_at desc: row[%d]=%d > row[%d]=%d",
							i, got[i].UpdatedAt, i-1, got[i-1].UpdatedAt)
					}
				}
			}
		})
	}
}

// ---------- readSessionIndex tests ----------

func TestCodexScanner_ReadSessionIndex(t *testing.T) {
	tests := map[string]struct {
		buildFS  func(t *testing.T, homeDir string)
		want     int
		wantKeys []string
	}{
		"missing_file_returns_empty": {
			want: 0,
		},
		"empty_file": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".codex")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: 0,
		},
		"single_entry": {
			buildFS: func(t *testing.T, homeDir string) {
				writeSessionIndexJSONL(t, homeDir, []sessionIndexEntry{
					{ID: "idx-1", Title: "Session One", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
				})
			},
			want: 1,
		},
		"multiple_entries": {
			buildFS: func(t *testing.T, homeDir string) {
				writeSessionIndexJSONL(t, homeDir, []sessionIndexEntry{
					{ID: "idx-1", Title: "Session One", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
					{ID: "idx-2", Title: "Session Two", UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
					{ID: "idx-3", Title: "Session Three", UpdatedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
				})
			},
			want: 3,
		},
		"malformed_lines_in_jsonl": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".codex")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				content := `{"id":"good","thread_name":"Good","updated_at":"2026-01-01T00:00:00Z"}
{bad json line
{"id":"also-good","thread_name":"Also Good","updated_at":"2026-01-02T00:00:00Z"}
`
				if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want:     2,
			wantKeys: []string{"good", "also-good"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			cs := newCodexScanner(homeDir)
			got, err := cs.readSessionIndex()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("expected %d entries, got %d: %+v", tt.want, len(got), got)
			}
			for _, key := range tt.wantKeys {
				if _, ok := got[key]; !ok {
					t.Errorf("expected key %q in map", key)
				}
			}
		})
	}
}

// ---------- parseRolloutMeta tests ----------

func TestCodexScanner_ParseRolloutMeta(t *testing.T) {
	now := time.Now()

	t.Run("valid_session_meta", func(t *testing.T) {
		homeDir := t.TempDir()
		path := writeRolloutFile(t, homeDir, "test-id", "/proj/test", "feat/foo",
			now.Add(-1*time.Hour), now)

		cs := newCodexScanner(homeDir)
		meta, err := cs.parseRolloutMeta(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.ID != "test-id" {
			t.Errorf("ID = %q, want %q", meta.ID, "test-id")
		}
		if meta.Cwd != "/proj/test" {
			t.Errorf("Cwd = %q, want %q", meta.Cwd, "/proj/test")
		}
		if meta.GitBranch != "feat/foo" {
			t.Errorf("GitBranch = %q, want %q", meta.GitBranch, "feat/foo")
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		cs := newCodexScanner(t.TempDir())
		_, err := cs.parseRolloutMeta("/nonexistent/rollout.jsonl")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("empty_file", func(t *testing.T) {
		homeDir := t.TempDir()
		path := filepath.Join(homeDir, "empty.jsonl")
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}

		cs := newCodexScanner(homeDir)
		_, err := cs.parseRolloutMeta(path)
		if err == nil {
			t.Fatal("expected error for empty file")
		}
	})

	t.Run("non_session_meta_first_line", func(t *testing.T) {
		homeDir := t.TempDir()
		path := filepath.Join(homeDir, "bad.jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"event_msg","timestamp":"2026-01-01T00:00:00Z"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cs := newCodexScanner(homeDir)
		_, err := cs.parseRolloutMeta(path)
		if err == nil {
			t.Fatal("expected error for non-session_meta first line")
		}
	})
}

// ---------- Constructor test ----------

func TestNewCodexScanner(t *testing.T) {
	cs := newCodexScanner("/home/testuser")
	if cs == nil {
		t.Fatal("newCodexScanner returned nil")
	}
	if cs.homeDir != "/home/testuser" {
		t.Errorf("homeDir = %q, want %q", cs.homeDir, "/home/testuser")
	}
}

// ---------- Aggregator wiring test ----------

func TestScanner_IncludesCodexScanner(t *testing.T) {
	s := New()
	if s.codex == nil {
		t.Fatal("New() did not initialize codex scanner")
	}
}
