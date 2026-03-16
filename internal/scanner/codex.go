package scanner

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/universe/claude-monitor/internal/model"

	// Register the pure-Go SQLite driver.
	_ "modernc.org/sqlite"
)

// codexScanner discovers OpenAI Codex CLI sessions by reading from
// ~/.codex/state_5.sqlite, ~/.codex/session_index.jsonl, and rollout
// JSONL files in ~/.codex/sessions/.
type codexScanner struct {
	// homeDir is the user's home directory, injected for testability.
	homeDir string
}

// newCodexScanner creates a codexScanner rooted at the given home directory.
func newCodexScanner(homeDir string) *codexScanner {
	return &codexScanner{homeDir: homeDir}
}

// scan discovers Codex CLI sessions and returns them as a slice of SessionInfo.
// It tries the SQLite database first; if the threads table is empty, it falls
// back to scanning rollout JSONL files directly.
func (c *codexScanner) scan(ctx context.Context) ([]model.SessionInfo, error) {
	// Try SQLite first.
	sessions, err := c.scanFromDB(ctx)
	if err != nil {
		return nil, err
	}
	if len(sessions) > 0 {
		return sessions, nil
	}

	// Fallback: scan rollout files when DB is empty/missing.
	return c.scanFromRolloutFiles(ctx)
}

// scanFromDB reads sessions from the SQLite threads table.
func (c *codexScanner) scanFromDB(ctx context.Context) ([]model.SessionInfo, error) {
	db, err := c.openDB()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("codex: open db: %w", err)
	}
	defer db.Close()

	rows, err := c.queryThreads(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("codex: query threads: %w", err)
	}

	if len(rows) == 0 {
		return nil, nil
	}

	index, _ := c.readSessionIndex() // best-effort

	now := time.Now()
	sessions := make([]model.SessionInfo, 0, len(rows))

	for _, r := range rows {
		updatedAt := time.Unix(r.UpdatedAt, 0)

		// For recent threads, check rollout for a newer timestamp.
		if r.RolloutPath != "" && time.Since(updatedAt) < 24*time.Hour {
			if ts := c.rolloutFileMtime(r.RolloutPath); ts.After(updatedAt) {
				updatedAt = ts
			}
		}

		// If session index has a more recent updated_at, use it.
		if entry, ok := index[r.ID]; ok && entry.UpdatedAt.After(updatedAt) {
			updatedAt = entry.UpdatedAt
		}

		title := r.Title
		if title == "" {
			if entry, ok := index[r.ID]; ok {
				title = entry.Title
			}
		}

		age := now.Sub(updatedAt)
		status := codexStatusFromAge(age)

		sessions = append(sessions, model.SessionInfo{
			ID:       r.ID,
			Provider: model.ProviderCodex,
			Status:   status,
			Title:    title,
			// InputTokens holds the combined total (input+output) since the
			// Codex DB stores a single tokens_used value without splitting.
			InputTokens: r.TokensUsed,
			ProjectDir:  r.Cwd,
			GitBranch:   r.GitBranch,
			LastActive:  updatedAt,
		})
	}

	return sessions, nil
}

// scanFromRolloutFiles discovers sessions by scanning rollout JSONL files
// in ~/.codex/sessions/. This is the fallback when the SQLite threads table
// is empty (which happens in Codex CLI 0.101+).
func (c *codexScanner) scanFromRolloutFiles(ctx context.Context) ([]model.SessionInfo, error) {
	sessionsDir := filepath.Join(c.homeDir, ".codex", "sessions")
	if _, err := os.Stat(sessionsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("codex: stat sessions dir: %w", err)
	}

	index, _ := c.readSessionIndex() // best-effort

	// Collect rollout files, most recent first (by mtime).
	type rolloutFile struct {
		path  string
		mtime time.Time
	}
	var files []rolloutFile

	err := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, rolloutFile{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("codex: walk sessions: %w", err)
	}

	// Sort by mtime descending and limit to most recent 50.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.After(files[j].mtime)
	})
	if len(files) > 50 {
		files = files[:50]
	}

	now := time.Now()
	sessions := make([]model.SessionInfo, 0, len(files))

	for _, rf := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		meta, err := c.parseRolloutMeta(rf.path)
		if err != nil {
			continue // skip unparseable files
		}

		// Use file mtime as the primary freshness signal.
		lastActive := rf.mtime

		// If session index has data, use it for title and potentially timestamp.
		title := ""
		if entry, ok := index[meta.ID]; ok {
			title = entry.Title
			if entry.UpdatedAt.After(lastActive) {
				lastActive = entry.UpdatedAt
			}
		}

		age := now.Sub(lastActive)
		status := codexStatusFromAge(age)

		sessions = append(sessions, model.SessionInfo{
			ID:           meta.ID,
			Provider:     model.ProviderCodex,
			Status:       status,
			Title:        title,
			InputTokens:  meta.InputTokens,
			OutputTokens: meta.OutputTokens,
			ProjectDir:   meta.Cwd,
			GitBranch:    meta.GitBranch,
			StartedAt:    meta.StartedAt,
			LastActive:   lastActive,
		})
	}

	return sessions, nil
}

// codexStatusFromAge maps the time since last activity to a status.
func codexStatusFromAge(age time.Duration) model.Status {
	switch {
	case age < 60*time.Second:
		return model.StatusActive
	case age < 10*time.Minute:
		return model.StatusIdle
	default:
		return model.StatusFinished
	}
}

// rolloutMeta holds parsed fields from a rollout file.
type rolloutMeta struct {
	ID           string
	Cwd          string
	GitBranch    string
	Source       string
	StartedAt    time.Time
	InputTokens  int64
	OutputTokens int64
}

// parseRolloutMeta reads a rollout JSONL file to extract session metadata
// from the first line (session_meta) and token usage from the last
// token_count event.
func (c *codexScanner) parseRolloutMeta(path string) (*rolloutMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return nil, fmt.Errorf("empty rollout file")
	}

	var header struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			Cwd       string `json:"cwd"`
			Source    string `json:"source"`
			Timestamp string `json:"timestamp"`
			Git       struct {
				Branch string `json:"branch"`
			} `json:"git"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return nil, fmt.Errorf("unmarshal session_meta: %w", err)
	}

	if header.Type != "session_meta" {
		return nil, fmt.Errorf("first line is %q, not session_meta", header.Type)
	}

	startedAt, _ := time.Parse(time.RFC3339Nano, header.Payload.Timestamp)

	meta := &rolloutMeta{
		ID:        header.Payload.ID,
		Cwd:       header.Payload.Cwd,
		GitBranch: header.Payload.Git.Branch,
		Source:    header.Payload.Source,
		StartedAt: startedAt,
	}

	// Scan remaining lines for the last token_count event.
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info struct {
					TotalTokenUsage struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type == "event_msg" && evt.Payload.Type == "token_count" {
			meta.InputTokens = evt.Payload.Info.TotalTokenUsage.InputTokens
			meta.OutputTokens = evt.Payload.Info.TotalTokenUsage.OutputTokens
		}
	}

	return meta, nil
}

// rolloutFileMtime returns the modification time of a rollout file.
func (c *codexScanner) rolloutFileMtime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// openDB opens the Codex state database in read-only mode.
func (c *codexScanner) openDB() (*sql.DB, error) {
	dbPath := filepath.Join(c.homeDir, ".codex", "state_5.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("stat db: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Verify the connection is usable.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

// queryThreads reads non-archived threads from the database.
func (c *codexScanner) queryThreads(ctx context.Context, db *sql.DB) ([]threadRow, error) {
	const query = `SELECT id, title, cwd, tokens_used, updated_at, git_branch, source, rollout_path
		FROM threads
		WHERE archived = 0
		ORDER BY updated_at DESC
		LIMIT 50`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query threads: %w", err)
	}
	defer rows.Close()

	var threads []threadRow
	for rows.Next() {
		var r threadRow
		var title, cwd, gitBranch, source, rolloutPath sql.NullString
		var tokensUsed sql.NullInt64
		if err := rows.Scan(&r.ID, &title, &cwd, &tokensUsed, &r.UpdatedAt, &gitBranch, &source, &rolloutPath); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		r.Title = title.String
		r.Cwd = cwd.String
		r.TokensUsed = tokensUsed.Int64
		r.GitBranch = gitBranch.String
		r.Source = source.String
		r.RolloutPath = rolloutPath.String
		threads = append(threads, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return threads, nil
}

// readSessionIndex reads additional metadata from session_index.jsonl.
func (c *codexScanner) readSessionIndex() (map[string]sessionIndexEntry, error) {
	path := filepath.Join(c.homeDir, ".codex", "session_index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open session index: %w", err)
	}
	defer f.Close()

	entries := make(map[string]sessionIndexEntry)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			ID        string `json:"id"`
			Title     string `json:"thread_name"`
			UpdatedAt string `json:"updated_at"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // skip malformed lines
		}
		if raw.ID == "" {
			continue
		}
		updatedAt, _ := time.Parse(time.RFC3339Nano, raw.UpdatedAt)
		entries[raw.ID] = sessionIndexEntry{
			ID:        raw.ID,
			Title:     raw.Title,
			UpdatedAt: updatedAt,
		}
	}
	if err := sc.Err(); err != nil {
		return entries, fmt.Errorf("read session index: %w", err)
	}
	return entries, nil
}

// sessionIndexEntry holds a single entry from session_index.jsonl.
type sessionIndexEntry struct {
	ID        string
	Title     string
	UpdatedAt time.Time
}

// threadRow represents a row from the threads table in the Codex state database.
type threadRow struct {
	ID          string
	Title       string
	Cwd         string
	TokensUsed  int64
	UpdatedAt   int64
	GitBranch   string
	Source      string
	RolloutPath string
}
