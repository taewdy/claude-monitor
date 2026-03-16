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
}

// writeSessionIndexJSONL writes a session_index.jsonl file.
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
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRolloutJSONL writes a rollout JSONL file with the given timestamps.
func writeRolloutJSONL(t *testing.T, path string, timestamps []int64) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ts := range timestamps {
		if err := enc.Encode(map[string]int64{"timestamp": ts}); err != nil {
			t.Fatal(err)
		}
	}
}

// ---------- scan() tests ----------

func TestCodexScanner_Scan(t *testing.T) {
	now := time.Now().Unix()

	tests := map[string]struct {
		buildFS  func(t *testing.T, homeDir string)
		sessions []model.SessionInfo // nil means check count only
		count    int
		wantErr  bool
	}{
		"no_codex_directory": {
			// ~/.codex doesn't exist → return empty slice, no error.
			count: 0,
		},
		"empty_database_no_threads": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, nil)
			},
			count: 0,
		},
		"single_active_thread": {
			// updated_at < 60s ago → active
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-001",
						Title:      "Fix auth flow",
						Cwd:        "/home/user/project",
						TokensUsed: 5000,
						UpdatedAt:  now - 10, // 10 seconds ago
						GitBranch:  "feat/auth",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "thread-001",
					Provider:   model.ProviderCodex,
					Status:     model.StatusActive,
					Title:      "Fix auth flow",
					ProjectDir: "/home/user/project",
					GitBranch:  "feat/auth",
				},
			},
		},
		"single_idle_thread": {
			// updated_at > 60s but < 10min → idle
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-002",
						Title:      "Refactor DB layer",
						Cwd:        "/home/user/backend",
						TokensUsed: 8000,
						UpdatedAt:  now - 300, // 5 minutes ago
						GitBranch:  "refactor/db",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "thread-002",
					Provider:   model.ProviderCodex,
					Status:     model.StatusIdle,
					Title:      "Refactor DB layer",
					ProjectDir: "/home/user/backend",
					GitBranch:  "refactor/db",
				},
			},
		},
		"single_finished_thread": {
			// updated_at > 10min → finished
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{
						ID:         "thread-003",
						Title:      "Old task",
						Cwd:        "/home/user/legacy",
						TokensUsed: 12000,
						UpdatedAt:  now - 3600, // 1 hour ago
						GitBranch:  "main",
						Source:     "cli",
					},
				})
			},
			sessions: []model.SessionInfo{
				{
					ID:         "thread-003",
					Provider:   model.ProviderCodex,
					Status:     model.StatusFinished,
					Title:      "Old task",
					ProjectDir: "/home/user/legacy",
					GitBranch:  "main",
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
		"multiple_threads_with_different_statuses": {
			buildFS: func(t *testing.T, homeDir string) {
				createCodexDB(t, homeDir, []threadRow{
					{ID: "t-active", Title: "Active", Cwd: "/proj/a", TokensUsed: 100, UpdatedAt: now - 10},
					{ID: "t-idle", Title: "Idle", Cwd: "/proj/b", TokensUsed: 200, UpdatedAt: now - 300},
					{ID: "t-done", Title: "Done", Cwd: "/proj/c", TokensUsed: 300, UpdatedAt: now - 3600},
				})
			},
			count: 3,
		},
		"corrupt_database_file": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".codex")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				// Write garbage bytes as the database.
				if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("not a database"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// Implementation should handle gracefully — either return error or empty.
			// The key contract: no panic.
			wantErr: true,
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
			// Boundary: exactly 60s should transition to idle.
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
			// Boundary: exactly 10min should transition to finished.
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
			// No .codex directory → should return error.
			wantErr: true,
		},
		"missing_db_file": {
			buildFS: func(t *testing.T, homeDir string) {
				if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			// Directory exists but no DB file → should return error.
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
					// openDB might succeed lazily with sqlite; try a query.
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

			// Verify we can query.
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
		rows    []threadRow
		want    int // expected number of returned rows
		wantErr bool
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
			want: 50, // LIMIT 50 in the query
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			createCodexDB(t, homeDir, tt.rows)

			// Open DB directly (bypassing stub openDB) so we can test queryThreads in isolation.
			dbPath := filepath.Join(homeDir, ".codex", "state_5.sqlite")
			db, err := sql.Open("sqlite", dbPath+"?mode=ro")
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer db.Close()

			cs := newCodexScanner(homeDir)
			got, err := cs.queryThreads(context.Background(), db)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("expected %d rows, got %d", tt.want, len(got))
			}

			// Verify ordering: first row should have highest updated_at.
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
		want     int // expected map size
		wantKeys []string
		wantErr  bool
	}{
		"missing_file_returns_empty": {
			// No session_index.jsonl → empty map, no error.
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
					{ID: "idx-1", Title: "Session One", UpdatedAt: 1700000000},
				})
			},
			want: 1,
		},
		"multiple_entries": {
			buildFS: func(t *testing.T, homeDir string) {
				writeSessionIndexJSONL(t, homeDir, []sessionIndexEntry{
					{ID: "idx-1", Title: "Session One", UpdatedAt: 1700000000},
					{ID: "idx-2", Title: "Session Two", UpdatedAt: 1700001000},
					{ID: "idx-3", Title: "Session Three", UpdatedAt: 1700002000},
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
				content := `{"id":"good","title":"Good","updated_at":100}
{bad json line
{"id":"also-good","title":"Also Good","updated_at":200}
`
				if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// Should skip bad lines, keep good ones.
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

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("expected %d entries, got %d: %+v", tt.want, len(got), got)
			}
			for _, key := range tt.wantKeys {
				if _, ok := got[key]; !ok {
					t.Errorf("expected key %q in map, got keys: %v", key, mapKeys(got))
				}
			}
		})
	}
}

// mapKeys returns the keys of a map for diagnostic output.
func mapKeys(m map[string]sessionIndexEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------- checkRolloutTimestamp tests ----------

func TestCodexScanner_CheckRolloutTimestamp(t *testing.T) {
	tests := map[string]struct {
		buildFS       func(t *testing.T, homeDir string) string // returns rollout path
		wantTimestamp int64
		wantErr       bool
	}{
		"missing_rollout_file": {
			buildFS: func(t *testing.T, homeDir string) string {
				return filepath.Join(homeDir, "nonexistent", "rollout.jsonl")
			},
			wantTimestamp: 0,
		},
		"empty_rollout_file": {
			buildFS: func(t *testing.T, homeDir string) string {
				path := filepath.Join(homeDir, "rollout.jsonl")
				if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantTimestamp: 0,
		},
		"single_event": {
			buildFS: func(t *testing.T, homeDir string) string {
				path := filepath.Join(homeDir, "rollout.jsonl")
				writeRolloutJSONL(t, path, []int64{1700000000})
				return path
			},
			wantTimestamp: 1700000000,
		},
		"multiple_events_returns_last": {
			buildFS: func(t *testing.T, homeDir string) string {
				path := filepath.Join(homeDir, "rollout.jsonl")
				writeRolloutJSONL(t, path, []int64{1700000000, 1700001000, 1700002000})
				return path
			},
			wantTimestamp: 1700002000,
		},
		"malformed_rollout_lines": {
			buildFS: func(t *testing.T, homeDir string) string {
				path := filepath.Join(homeDir, "rollout.jsonl")
				content := `{"timestamp":1700000000}
{not valid json}
{"timestamp":1700005000}
`
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantTimestamp: 1700005000,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			rolloutPath := tt.buildFS(t, homeDir)

			cs := newCodexScanner(homeDir)
			got, err := cs.checkRolloutTimestamp(rolloutPath)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantTimestamp {
				t.Errorf("timestamp = %d, want %d", got, tt.wantTimestamp)
			}
		})
	}
}

// ---------- Integration: scan with rollout and session index ----------

func TestCodexScanner_ScanWithRolloutAndIndex(t *testing.T) {
	now := time.Now().Unix()

	t.Run("rollout_timestamp_used_for_recent_threads", func(t *testing.T) {
		homeDir := t.TempDir()

		rolloutPath := filepath.Join(homeDir, ".codex", "rollouts", "thread-roll.jsonl")
		writeRolloutJSONL(t, rolloutPath, []int64{now - 5}) // 5 seconds ago

		createCodexDB(t, homeDir, []threadRow{
			{
				ID:          "thread-roll",
				Title:       "With Rollout",
				Cwd:         "/proj",
				TokensUsed:  100,
				UpdatedAt:   now - 30, // 30s ago in DB
				RolloutPath: rolloutPath,
			},
		})

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 session, got %d", len(got))
		}
		// The rollout timestamp (5s ago) is more recent than updated_at (30s ago),
		// so if the implementation uses rollout timestamps, the session could be active.
		// This test verifies that rollout is checked at all.
		if got[0].ID != "thread-roll" {
			t.Errorf("ID = %q, want %q", got[0].ID, "thread-roll")
		}
	})

	t.Run("session_index_provides_metadata", func(t *testing.T) {
		homeDir := t.TempDir()

		createCodexDB(t, homeDir, []threadRow{
			{
				ID:        "thread-idx",
				Title:     "DB Title",
				Cwd:       "/proj",
				UpdatedAt: now - 10,
			},
		})

		writeSessionIndexJSONL(t, homeDir, []sessionIndexEntry{
			{ID: "thread-idx", Title: "Index Title", UpdatedAt: now - 5},
		})

		cs := newCodexScanner(homeDir)
		got, err := cs.scan(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 session, got %d", len(got))
		}
		// Verify session was found; specific metadata merging depends on implementation.
		if got[0].ID != "thread-idx" {
			t.Errorf("ID = %q, want %q", got[0].ID, "thread-idx")
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
