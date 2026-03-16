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
		"no_sessions_directory": {
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"empty_sessions_directory": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "sessions")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"single_finished_session_with_conversation": {
			buildFS: func(t *testing.T, homeDir string) {
				cwd := "/home/user/myproject"
				sid := "sess-abc-123"
				writeSessionFile(t, homeDir, sid+".json", sessionFile{
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
		"malformed_session_json_skipped": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "sessions")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid json!!!"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"session_with_missing_conversation_jsonl": {
			buildFS: func(t *testing.T, homeDir string) {
				writeSessionFile(t, homeDir, "sess-no-conv.json", sessionFile{
					PID:       999999999,
					SessionID: "sess-no-conv",
					Cwd:       "/tmp/proj",
					StartedAt: startedAt.UnixMilli(),
				})
			},
			wants: wants{
				sessions: []model.SessionInfo{
					{
						ID:         "sess-no-conv",
						Provider:   model.ProviderClaude,
						Status:     model.StatusFinished,
						ProjectDir: "/tmp/proj",
						StartedAt:  startedAt,
						PID:        999999999,
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
				writeSessionFile(t, homeDir, sid+".json", sessionFile{
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
		"multiple_sessions": {
			buildFS: func(t *testing.T, homeDir string) {
				for i, sid := range []string{"sess-001", "sess-002", "sess-003"} {
					cwd := fmt.Sprintf("/proj/%d", i)
					writeSessionFile(t, homeDir, sid+".json", sessionFile{
						PID:       999999999 - i,
						SessionID: sid,
						Cwd:       cwd,
						StartedAt: startedAt.UnixMilli(),
					})
				}
			},
			wants: wants{
				count:  3,
				errNil: true,
			},
		},
		"non_json_files_in_sessions_dir_ignored": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "sessions")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a session"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: wants{
				count:  0,
				errNil: true,
			},
		},
		"session_with_invalid_started_at": {
			buildFS: func(t *testing.T, homeDir string) {
				dir := filepath.Join(homeDir, ".claude", "sessions")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				data := `{"pid":999999999,"sessionId":"sess-bad-time","cwd":"/tmp","startedAt":"not-a-date"}`
				if err := os.WriteFile(filepath.Join(dir, "sess-bad-time.json"), []byte(data), 0o644); err != nil {
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
				writeSessionFile(t, homeDir, sid+".json", sessionFile{
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
				writeSessionFile(t, homeDir, sid+".json", sessionFile{
					PID:       999999999,
					SessionID: sid,
					Cwd:       cwd,
					StartedAt: startedAt.UnixMilli(),
				})
				encoded := encodeCwd(cwd)
				// Write JSONL manually with some bad lines interspersed.
				dir := filepath.Join(homeDir, ".claude", "projects", encoded)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, sid+".jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				// Valid line
				validMsg := mkMsg("user", staleTimestamp, &usage{InputTokens: 100, OutputTokens: 0})
				validData, _ := json.Marshal(validMsg)
				f.Write(validData)
				f.WriteString("\n")
				// Malformed line
				f.WriteString("{this is not valid json\n")
				// Another valid line
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
						MessageCount: 2, // only the 2 valid lines
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
				writeSessionFile(t, homeDir, sid+".json", sessionFile{
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
				// Create empty JSONL file
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
				writeSessionFile(t, homeDir, "sess-cancel.json", sessionFile{
					PID:       999999999,
					SessionID: "sess-cancel",
					Cwd:       "/tmp",
					StartedAt: startedAt.UnixMilli(),
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

// TestClaudeScanner_StatusDetermination tests the four status rules:
//   - PID dead → finished
//   - PID alive + last msg < 60s → active
//   - PID alive + last msg > 60s → idle
//   - PID alive + last msg is assistant role → waiting
//
// We use os.Getpid() as a known-alive PID for the live-PID cases.
func TestClaudeScanner_StatusDetermination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-1 * time.Hour)
	alivePID := os.Getpid()

	// makeSession is a helper that writes session + optional JSONL for a given
	// pid and message list, reducing boilerplate across status test cases.
	makeSession := func(t *testing.T, homeDir, sid string, pid int, msgs []jsonlMessage) {
		t.Helper()
		cwd := "/tmp/" + sid
		writeSessionFile(t, homeDir, sid+".json", sessionFile{
			PID: pid, SessionID: sid, Cwd: cwd,
			StartedAt: startedAt.UnixMilli(),
		})
		if len(msgs) > 0 {
			writeConversationJSONL(t, homeDir, encodeCwd(cwd), sid, msgs)
		}
	}

	tests := map[string]struct {
		buildFS func(t *testing.T, homeDir string)
		status  model.Status
	}{
		"dead_pid_is_finished": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "dead", 999999999, nil)
			},
			status: model.StatusFinished,
		},
		"alive_pid_recent_user_msg_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "active", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-5*time.Second), nil),
				})
			},
			status: model.StatusActive,
		},
		"alive_pid_stale_msg_is_idle": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "idle", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-5*time.Minute), nil),
				})
			},
			status: model.StatusIdle,
		},
		"alive_pid_last_msg_assistant_is_waiting": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "waiting", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-5*time.Minute), nil),
					mkMsg("assistant", now.Add(-2*time.Minute), nil),
				})
			},
			status: model.StatusWaiting,
		},
		"alive_pid_no_messages_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "no-msgs", alivePID, nil)
			},
			status: model.StatusActive,
		},
		"alive_pid_recent_assistant_msg_is_active": {
			buildFS: func(t *testing.T, homeDir string) {
				makeSession(t, homeDir, "recent-asst", alivePID, []jsonlMessage{
					mkMsg("user", now.Add(-10*time.Second), nil),
					mkMsg("assistant", now.Add(-5*time.Second), nil),
				})
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
			inputTokens:  10 * 10, // 10 per line
			outputTokens: 10 * 20, // 20 per line
		},
		"large_file_only_tail_parsed": {
			// Only the last ~50 lines should be parsed.
			// Token totals should reflect ~50 lines, not all 100.
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

			writeSessionFile(t, homeDir, sid+".json", sessionFile{
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
// from JSONL messages. The last slug value found in the tail should be used as
// the session Title.
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

			writeSessionFile(t, homeDir, sid+".json", sessionFile{
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

// TestClaudeScanner_EncodedCwdPath verifies that the scanner correctly encodes
// the cwd path to find the projects directory. Claude Code replaces "/" with "-".
func TestClaudeScanner_EncodedCwdPath(t *testing.T) {
	startedAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	const wantMsgCount = 2

	tests := map[string]string{
		"simple_path":        "/home/user/project",
		"path_with_spaces":   "/home/user/my project",
		"root_path":          "/",
		"deeply_nested_path": "/a/b/c/d/e/f/g",
	}

	for name, cwd := range tests {
		t.Run(name, func(t *testing.T) {
			homeDir := t.TempDir()
			sid := "sess-path-test"

			writeSessionFile(t, homeDir, sid+".json", sessionFile{
				PID:       999999999,
				SessionID: sid,
				Cwd:       cwd,
				StartedAt: startedAt.UnixMilli(),
			})

			encoded := encodeCwd(cwd)
			writeConversationJSONL(t, homeDir, encoded, sid, []jsonlMessage{
				mkMsg("user", startedAt.Add(1*time.Minute), nil),
				mkMsg("assistant", startedAt.Add(2*time.Minute), nil),
			})

			s := scanSingle(t, homeDir)
			if s.MessageCount != wantMsgCount {
				t.Errorf("messageCount = %d, want %d (conversation may not have been found)", s.MessageCount, wantMsgCount)
			}
		})
	}
}
