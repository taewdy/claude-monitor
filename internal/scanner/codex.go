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
	"time"

	"github.com/universe/claude-monitor/internal/model"

	// Register the pure-Go SQLite driver.
	_ "modernc.org/sqlite"
)

// codexScanner discovers OpenAI Codex CLI sessions by reading from
// ~/.codex/state_5.sqlite and ~/.codex/session_index.jsonl.
type codexScanner struct {
	// homeDir is the user's home directory, injected for testability.
	homeDir string
}

// newCodexScanner creates a codexScanner rooted at the given home directory.
func newCodexScanner(homeDir string) *codexScanner {
	return &codexScanner{homeDir: homeDir}
}

// scan discovers Codex CLI sessions from the local SQLite database and
// returns them as a slice of SessionInfo.
func (c *codexScanner) scan(ctx context.Context) ([]model.SessionInfo, error) {
	db, err := c.openDB()
	if err != nil {
		// Missing ~/.codex or missing DB is not an error — just no sessions.
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

	index, _ := c.readSessionIndex() // best-effort; ignore errors

	now := time.Now().Unix()
	sessions := make([]model.SessionInfo, 0, len(rows))

	for _, r := range rows {
		updatedAt := r.UpdatedAt

		// For recent threads (updated in last 24h), check rollout for a newer timestamp.
		if r.RolloutPath != "" && (now-updatedAt) < 86400 {
			if ts, err := c.checkRolloutTimestamp(r.RolloutPath); err == nil && ts > updatedAt {
				updatedAt = ts
			}
		}

		// If session index has a more recent updated_at, use it.
		if entry, ok := index[r.ID]; ok && entry.UpdatedAt > updatedAt {
			updatedAt = entry.UpdatedAt
		}

		age := now - updatedAt
		var status model.Status
		switch {
		case age < 60:
			status = model.StatusActive
		case age < 600:
			status = model.StatusIdle
		default:
			status = model.StatusFinished
		}

		sessions = append(sessions, model.SessionInfo{
			ID:       r.ID,
			Provider: model.ProviderCodex,
			Status:   status,
			Title:    r.Title,
			// InputTokens holds the combined total (input+output) since the
			// Codex DB stores a single tokens_used value without splitting.
			InputTokens: r.TokensUsed,
			ProjectDir:  r.Cwd,
			GitBranch:   r.GitBranch,
			LastActive:  time.Unix(updatedAt, 0),
		})
	}

	return sessions, nil
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

// readSessionIndex reads additional metadata from session_index.jsonl if it exists.
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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry sessionIndexEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}
		if entry.ID != "" {
			entries[entry.ID] = entry
		}
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("read session index: %w", err)
	}
	return entries, nil
}

// checkRolloutTimestamp reads the last event timestamp from a rollout JSONL file.
func (c *codexScanner) checkRolloutTimestamp(rolloutPath string) (int64, error) {
	f, err := os.Open(rolloutPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("open rollout: %w", err)
	}
	defer f.Close()

	var lastTS int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event struct {
			Timestamp int64 `json:"timestamp"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue // skip malformed lines
		}
		if event.Timestamp > 0 {
			lastTS = event.Timestamp
		}
	}
	if err := scanner.Err(); err != nil {
		return lastTS, fmt.Errorf("read rollout: %w", err)
	}
	return lastTS, nil
}

// sessionIndexEntry holds a single entry from session_index.jsonl.
type sessionIndexEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updated_at"`
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
