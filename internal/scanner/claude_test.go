package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

// sessionFile represents the JSON structure of a Claude Code session file
// at ~/.claude/sessions/*.json. This is the contract the scanner must parse.
type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
}

// jsonlMessage represents a single line in a conversation JSONL file.
// Matches the actual Claude Code format with nested message object and snake_case fields.
type jsonlMessage struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Slug      string         `json:"slug,omitempty"`
	Cwd       string         `json:"cwd,omitempty"`
	Message   *nestedMessage `json:"message,omitempty"`
}

type nestedMessage struct {
	Role  string `json:"role"`
	Usage *usage `json:"usage,omitempty"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
}

// mkMsg creates a jsonlMessage with the nested structure matching Claude Code's actual format.
func mkMsg(role string, ts time.Time, u *usage) jsonlMessage {
	return jsonlMessage{
		Type:      role,
		Timestamp: ts,
		Message:   &nestedMessage{Role: role, Usage: u},
	}
}

// mkMsgWithCwd creates a jsonlMessage with a cwd field (like real user messages have).
func mkMsgWithCwd(role string, ts time.Time, u *usage, cwd string) jsonlMessage {
	m := mkMsg(role, ts, u)
	m.Cwd = cwd
	return m
}

// mkMsgSlug creates a jsonlMessage with a slug/title.
func mkMsgSlug(role string, ts time.Time, u *usage, slug string) jsonlMessage {
	m := mkMsg(role, ts, u)
	m.Slug = slug
	return m
}

// helper: write a session JSON file into the temp home dir.
func writeSessionFile(t *testing.T, homeDir string, filename string, sf sessionFile) {
	t.Helper()
	dir := filepath.Join(homeDir, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// helper: write a conversation JSONL file into the temp home dir.
func writeConversationJSONL(t *testing.T, homeDir, encodedCwd, sessionID string, messages []jsonlMessage) {
	t.Helper()
	dir := filepath.Join(homeDir, ".claude", "projects", encodedCwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, msg := range messages {
		if err := enc.Encode(msg); err != nil {
			t.Fatal(err)
		}
	}
}

// helper: create subagent files for a session.
func writeSubagentFiles(t *testing.T, homeDir, encodedCwd, sessionID string, count int) {
	t.Helper()
	dir := filepath.Join(homeDir, ".claude", "projects", encodedCwd, sessionID, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		name := filepath.Join(dir, fmt.Sprintf("subagent_%d.jsonl", i))
		if err := os.WriteFile(name, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// encodeCwd encodes a filesystem path the way Claude Code does: replacing
// "/" with "-" to produce a flat directory name.
func encodeCwd(path string) string {
	return strings.ReplaceAll(path, "/", "-")
}

// setFileMtime sets the modification time on the JSONL file for the given session.
func setFileMtime(t *testing.T, homeDir, sid, cwd string, mtime time.Time) {
	t.Helper()
	encoded := encodeCwd(cwd)
	jsonlPath := filepath.Join(homeDir, ".claude", "projects", encoded, sid+".jsonl")
	if err := os.Chtimes(jsonlPath, mtime, mtime); err != nil {
		t.Fatalf("setFileMtime: %v", err)
	}
}

// scanSingle runs the claude scanner on the given homeDir, asserts no error and
// exactly one result, then returns that single session.
func scanSingle(t *testing.T, homeDir string) model.SessionInfo {
	t.Helper()
	cs := newClaudeScanner(homeDir)
	got, err := cs.scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 session, got %d", len(got))
	}
	return got[0]
}

// assertSession compares all fields of a SessionInfo against expected values.
func assertSession(t *testing.T, idx int, got, want model.SessionInfo) {
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
	if got.ProjectDir != want.ProjectDir {
		t.Errorf("session[%d].ProjectDir = %q, want %q", idx, got.ProjectDir, want.ProjectDir)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("session[%d].StartedAt = %v, want %v", idx, got.StartedAt, want.StartedAt)
	}
	if !got.LastActive.Equal(want.LastActive) {
		t.Errorf("session[%d].LastActive = %v, want %v", idx, got.LastActive, want.LastActive)
	}
	if got.InputTokens != want.InputTokens {
		t.Errorf("session[%d].InputTokens = %d, want %d", idx, got.InputTokens, want.InputTokens)
	}
	if got.OutputTokens != want.OutputTokens {
		t.Errorf("session[%d].OutputTokens = %d, want %d", idx, got.OutputTokens, want.OutputTokens)
	}
	if got.MessageCount != want.MessageCount {
		t.Errorf("session[%d].MessageCount = %d, want %d", idx, got.MessageCount, want.MessageCount)
	}
	if got.SubagentCount != want.SubagentCount {
		t.Errorf("session[%d].SubagentCount = %d, want %d", idx, got.SubagentCount, want.SubagentCount)
	}
	if got.PID != want.PID {
		t.Errorf("session[%d].PID = %d, want %d", idx, got.PID, want.PID)
	}
	if got.Title != want.Title {
		t.Errorf("session[%d].Title = %q, want %q", idx, got.Title, want.Title)
	}
}

func TestClaudeScanner_Scan(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	recentTimestamp := now.Add(-10 * time.Second)
	staleTimestamp := now.Add(-5 * time.Minute)
	startedAt := now.Add(-1 * time.Hour)

	type wants struct {
		sessions []model.SessionInfo
		count    int // used when sessions is nil but we expect a specific count
		errNil   bool
	}

	tests := map[string]struct {
		ctx     context.Context // nil defaults to context.Background()
		buildFS func(t *testing.T, homeDir string)
		wants   wants
	}{
		"no_projects_directory": {
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"empty_projects_directory": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "projects")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"finished_session_with_conversation": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/myproject"
				sid := "sess-abc-123"
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
					mkMsg("user", startedAt.Add(1*time.Minute), &usage{InputTokens: 100, OutputTokens: 0}),
					mkMsg("assistant", startedAt.Add(2*time.Minute), &usage{InputTokens: 0, OutputTokens: 250}),
					mkMsg("user", staleTimestamp, &usage{InputTokens: 150, OutputTokens: 0}),
				})
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:           "sess-abc-123",
						Provider:     model.ProviderClaude,
						Status:       model.StatusFinished,
						ProjectDir:   "/home/user/myproject",
						StartedAt:    startedAt,
						LastActive:   staleTimestamp,
						InputTokens:  250,
						OutputTokens: 250,
						MessageCount: 3,
						PID:          999999999,
					},
				},
				errNil: true,
			},
		},
		"jsonl_without_session_file": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/myproject"
				sid := "sess-orphan-001"
				encoded := encodeCwd(cwd)
				writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
					mkMsgWithCwd("user", staleTimestamp.Add(-1*time.Minute), &usage{InputTokens: 100, OutputTokens: 0}, cwd),
					mkMsg("assistant", staleTimestamp, &usage{InputTokens: 0, OutputTokens: 200}),
				})
				setFileMtime(t, homeDir, sid, cwd, staleTimestamp)
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:           "sess-orphan-001",
						Provider:     model.ProviderClaude,
						Status:       model.StatusFinished, // no PID, stale → finished
						ProjectDir:   "/home/user/myproject",
						StartedAt:    staleTimestamp.Add(-1 * time.Minute), // from first timestamp
						LastActive:   staleTimestamp,
						InputTokens:  100,
						OutputTokens: 200,
						MessageCount: 2,
					},
				},
				errNil: true,
			},
		},
		"session_with_subagents": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/proj"
				sid := "sess-sub-001"
				ts := startedAt.Add(1 * time.Minute)
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
					mkMsg("user", ts, nil),
				})
				writeSubagentFiles(t, homeDir, encoded, sid, 3)
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:            "sess-sub-001",
						Provider:      model.ProviderClaude,
						Status:        model.StatusFinished,
						ProjectDir:    "/home/user/proj",
						StartedAt:     startedAt,
						LastActive:    startedAt.Add(1 * time.Minute),
						MessageCount:  1,
						SubagentCount: 3,
						PID:           999999999,
					},
				},
				errNil: true,
			},
		},
		"multiple_jsonl_files_in_project": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/multi"
				encoded := encodeCwd(cwd)
				for i, sid := range []string{"sess-001", "sess-002", "sess-003"} {
					writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
						mkMsgWithCwd("user", staleTimestamp.Add(time.Duration(i)*time.Second), nil, cwd),
					})
				}
			},
			wants: wants{
				count:  3,
				errNil: true,
			},
		},
		"non_jsonl_files_in_project_dir_ignored": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "projects", "some-project")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a session"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"token_accumulation_from_jsonl": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/tokens"
				sid := "sess-tokens"
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
					mkMsg("user", recentTimestamp.Add(-3*time.Second), &usage{InputTokens: 100, OutputTokens: 0}),
					mkMsg("assistant", recentTimestamp.Add(-2*time.Second), &usage{InputTokens: 0, OutputTokens: 500}),
					mkMsg("user", recentTimestamp.Add(-1*time.Second), &usage{InputTokens: 200, OutputTokens: 0}),
					mkMsg("assistant", recentTimestamp, &usage{InputTokens: 0, OutputTokens: 800}),
				})
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:           "sess-tokens",
						Provider:     model.ProviderClaude,
						Status:       model.StatusFinished,
						ProjectDir:   "/home/user/tokens",
						StartedAt:    startedAt,
						LastActive:   recentTimestamp,
						InputTokens:  300,
						OutputTokens: 1300,
						MessageCount: 4,
						PID:          999999999,
					},
				},
				errNil: true,
			},
		},
		"malformed_jsonl_lines_skipped": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/badlines"
				sid := "sess-bad-jsonl"
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				dir := filepath.Join(homeDir, ".claude", "projects", encoded)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, sid+".jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				validMsg := mkMsg("user", staleTimestamp, &usage{InputTokens: 100, OutputTokens: 0})
				validData, _ := json.Marshal(validMsg)
				f.Write(validData)
				f.WriteString("\n")
				f.WriteString("{this is not valid json\n")
				validMsg2 := mkMsg("assistant", staleTimestamp.Add(time.Second), &usage{InputTokens: 0, OutputTokens: 200})
				validData2, _ := json.Marshal(validMsg2)
				f.Write(validData2)
				f.WriteString("\n")
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:           "sess-bad-jsonl",
						Provider:     model.ProviderClaude,
						Status:       model.StatusFinished,
						ProjectDir:   "/home/user/badlines",
						StartedAt:    startedAt,
						LastActive:   staleTimestamp.Add(time.Second),
						InputTokens:  100,
						OutputTokens: 200,
						MessageCount: 2,
						PID:          999999999,
					},
				},
				errNil: true,
			},
		},
		"empty_jsonl_file": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/empty-conv"
				sid := "sess-empty-jsonl"
				writeSessionFile(t, homeDir, "999999999.json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				dir := filepath.Join(homeDir, ".claude", "projects", encoded)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:         "sess-empty-jsonl",
						Provider:   model.ProviderClaude,
						Status:     model.StatusFinished,
						ProjectDir: "/home/user/empty-conv",
						StartedAt:  startedAt,
						PID:        999999999,
					},
				},
				errNil: true,
			},
		},
		"context_cancelled": {
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/tmp"
				encoded := encodeCwd(cwd)
				writeConversationJSONL(t, homeDir, encoded, "sess-cancel", []jsonlMessage{
					mkMsg("user", staleTimestamp, nil),
				})
			},
			wants: wants{
				errNil: false,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.buildFS != nil {
				tt.buildFS(t, homeDir)
			}

			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}

			cs := newClaudeScanner(homeDir)
			got, err := cs.scan(ctx)

			if tt.wants.errNil {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}

			if tt.wants.sessions != nil {
				if len(got) != len(tt.wants.sessions) {
					t.Fatalf("expected %d sessions, got %d: %+v", len(tt.wants.sessions), len(got), got)
				}
				for i, want := range tt.wants.sessions {
					assertSession(t, i, got[i], want)
				}
				return
			}

			if len(got) != tt.wants.count {
				t.Fatalf("expected %d sessions, got %d: %+v", tt.wants.count, len(got), got)
			}
		})
	}
}

// TestClaudeScanner_StatusDetermination tests the status priority rules:
//  1. PID known and dead → finished
//  2. JSONL file modified within 2 min → active
//  3. JSONL mtime is zero + PID alive → active (brand new)
//  4. JSONL mtime is zero + no PID → finished
//  5. Stale JSONL + no PID → finished
//  6. Stale JSONL + PID alive + last role "assistant" → waiting
//  7. Stale JSONL + PID alive → idle
func TestClaudeScanner_StatusDetermination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-1 * time.Hour)
	staleTimestamp := now.Add(-10 * time.Minute)

	// Use a known-alive PID for tests that need an alive process.
	alivePID := os.Getpid()

	// makeSession writes a session file + JSONL for the given pid and messages.
	makeSession := func(t *testing.T, homeDir, sid string, pid int, msgs []jsonlMessage) {
		t.Helper()
		cwd := "/tmp/" + sid
		writeSessionFile(t, homeDir, fmt.Sprintf("%d.json", pid), sessionFile{
			PID: pid, SessionID: sid, Cwd: cwd,
			StartedAt: startedAt.UnixMilli(),
		})
		if len(msgs) > 0 {
			writeConversationJSONL(t, homeDir, encodeCwd(cwd), sid, msgs)
		}
	}

	// makeJSONLOnly writes a JSONL without a session file (like claude -p).
	makeJSONLOnly := func(t *testing.T, homeDir, sid string, msgs []jsonlMessage, cwd string) {
		t.Helper()
		writeConversationJSONL(t, homeDir, encodeCwd(cwd), sid, msgs)
	}

	tests := map[string]struct {
		buildFS func(t *testing.T, homeDir string)
		status  model.Status
	}{
		"dead_pid_is_finished": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "dead", 999999999, []jsonlMessage{
					mkMsg("user", staleTimestamp, nil),
				})
			},
			status: model.StatusFinished,
		},
		"alive_pid_recent_file_mtime_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				// File just written, mtime is fresh.
				makeSession(t, homeDir, "recent", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-3*time.Minute), nil),
				})
			},
			status: model.StatusActive,
		},
		"alive_pid_stale_msg_is_idle": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "idle"
				cwd := "/tmp/" + sid
				makeSession(t, homeDir, sid, alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-10*time.Minute), nil),
				})
				setFileMtime(t, homeDir, sid, cwd, now.Add(-10*time.Minute))
			},
			status: model.StatusIdle,
		},
		"alive_pid_last_msg_assistant_is_waiting": {
			buildFS: func(t *testing.T, homeDir string) {
				sid := "waiting"
				cwd := "/tmp/" + sid
				makeSession(t, homeDir, sid, alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-10*time.Minute), nil),
					mkMsg("assistant", now.Add(-6*time.Minute), nil),
				})
				setFileMtime(t, homeDir, sid, cwd, now.Add(-6*time.Minute))
			},
			status: model.StatusWaiting,
		},
		"alive_pid_recent_assistant_msg_within_2min_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				// File just written, mtime is fresh.
				makeSession(t, homeDir, "recent-asst", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-4*time.Minute), nil),
					mkMsg("assistant", now.Add(-2*time.Minute), nil),
				})
			},
			status: model.StatusActive,
		},
		"no_pid_stale_mtime_is_finished": {
			buildFS: func(t *testing.T, homeDir string) {
				// Orphan JSONL (like claude -p that completed).
				sid := "orphan"
				cwd := "/tmp/" + sid
				makeJSONLOnly(t, homeDir, sid, []jsonlMessage{
					mkMsgWithCwd("user", staleTimestamp, nil, cwd),
					mkMsg("assistant", staleTimestamp.Add(time.Second), nil),
				}, cwd)
				setFileMtime(t, homeDir, sid, cwd, staleTimestamp)
			},
			status: model.StatusFinished,
		},
		"no_pid_fresh_mtime_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				// Orphan JSONL being actively written (claude -p in progress).
				sid := "orphan-active"
				cwd := "/tmp/" + sid
				makeJSONLOnly(t, homeDir, sid, []jsonlMessage{
					mkMsgWithCwd("user", now, nil, cwd),
				}, cwd)
				// Don't set mtime — file was just created, mtime is fresh.
			},
			status: model.StatusActive,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			tt.buildFS(t, homeDir)

			cs := newClaudeScanner(homeDir)
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

// TestClaudeScanner_TailParsing verifies that the scanner only reads the tail
// (~50 lines) of a large JSONL and accumulates tokens from what it reads.
func TestClaudeScanner_TailParsing(t *testing.T) {
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)

	tests := map[string]struct {
		totalLines   int
		inputTokens  int64
		outputTokens int64
	}{
		"small_file_all_lines_parsed": {
			totalLines:   10,
			inputTokens:  10 * 10,
			outputTokens: 10 * 20,
		},
		"large_file_only_tail_parsed": {
			totalLines:   100,
			inputTokens:  50 * 10,
			outputTokens: 50 * 20,
		},
		"exactly_50_lines": {
			totalLines:   50,
			inputTokens:  50 * 10,
			outputTokens: 50 * 20,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			cwd := "/tmp/tail-test"
			sid := "sess-tail"

			writeSessionFile(t, homeDir, "999999999.json", sessionFile{
				PID:       999999999,
				SessionID: sid,
				Cwd:       cwd,
				StartedAt: startedAt.UnixMilli(),
			})

			messages := make([]jsonlMessage, tt.totalLines)
			for i := range tt.totalLines {
				role := "user"
				if i%2 == 1 {
					role = "assistant"
				}
				messages[i] = mkMsg(role, startedAt.Add(time.Duration(i)*time.Second), &usage{InputTokens: 10, OutputTokens: 20})
			}

			encoded := encodeCwd(cwd)
			writeConversationJSONL(t, homeDir, encoded, sid, messages)

			s := scanSingle(t, homeDir)
			if s.InputTokens != tt.inputTokens {
				t.Errorf("InputTokens = %d, want %d", s.InputTokens, tt.inputTokens)
			}
			if s.OutputTokens != tt.outputTokens {
				t.Errorf("OutputTokens = %d, want %d", s.OutputTokens, tt.outputTokens)
			}
		})
	}
}

// TestClaudeScanner_SlugTitle verifies that the scanner extracts the slug/title
// from JSONL messages.
func TestClaudeScanner_SlugTitle(t *testing.T) {
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)

	tests := map[string]struct {
		messages  []jsonlMessage
		wantTitle string
	}{
		"slug_present_on_message": {
			messages: []jsonlMessage{
				mkMsgSlug("user", startedAt.Add(time.Minute), nil, "fix-auth-bug"),
				mkMsg("assistant", startedAt.Add(2*time.Minute), nil),
			},
			wantTitle: "fix-auth-bug",
		},
		"last_slug_wins": {
			messages: []jsonlMessage{
				mkMsgSlug("user", startedAt.Add(time.Minute), nil, "initial-title"),
				mkMsgSlug("assistant", startedAt.Add(2*time.Minute), nil, "updated-title"),
			},
			wantTitle: "updated-title",
		},
		"no_slug_means_empty_title": {
			messages: []jsonlMessage{
				mkMsg("user", startedAt.Add(time.Minute), nil),
				mkMsg("assistant", startedAt.Add(2*time.Minute), nil),
			},
			wantTitle: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			cwd := "/tmp/slug-test"
			sid := "sess-slug"

			writeSessionFile(t, homeDir, "999999999.json", sessionFile{
				PID:       999999999,
				SessionID: sid,
				Cwd:       cwd,
				StartedAt: startedAt.UnixMilli(),
			})

			encoded := encodeCwd(cwd)
			writeConversationJSONL(t, homeDir, encoded, sid, tt.messages)

			s := scanSingle(t, homeDir)
			if s.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", s.Title, tt.wantTitle)
			}
		})
	}
}

// TestClaudeScanner_CwdFromJSONL verifies that the scanner extracts cwd from
// JSONL user messages when no session file is available.
func TestClaudeScanner_CwdFromJSONL(t *testing.T) {
	staleTimestamp := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	cwd := "/home/user/project"
	sid := "sess-cwd"

	homeDir := t.TempDir()
	encoded := encodeCwd(cwd)
	writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
		mkMsgWithCwd("user", staleTimestamp, nil, cwd),
		mkMsg("assistant", staleTimestamp.Add(time.Second), nil),
	})

	s := scanSingle(t, homeDir)
	if s.ProjectDir != cwd {
		t.Errorf("ProjectDir = %q, want %q", s.ProjectDir, cwd)
	}
}

// TestClaudeScanner_SessionFileEnrichment verifies that PID and StartedAt
// come from the session file when available.
func TestClaudeScanner_SessionFileEnrichment(t *testing.T) {
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	staleTimestamp := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	cwd := "/home/user/project"
	sid := "sess-enriched"

	homeDir := t.TempDir()
	writeSessionFile(t, homeDir, "42.json", sessionFile{
		PID:       999999999,
		SessionID: sid,
		Cwd:       cwd,
		StartedAt: startedAt.UnixMilli(),
	})
	encoded := encodeCwd(cwd)
	writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
		mkMsg("user", staleTimestamp, nil),
	})

	s := scanSingle(t, homeDir)
	if s.PID != 999999999 {
		t.Errorf("PID = %d, want 999999999", s.PID)
	}
	if !s.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v (from session file)", s.StartedAt, startedAt)
	}
	if s.ProjectDir != cwd {
		t.Errorf("ProjectDir = %q, want %q (from session file)", s.ProjectDir, cwd)
	}
}
