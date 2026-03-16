// Package scanner provides session discovery for AI coding assistants.
package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

const tailLines = 50

// claudeScanner discovers Claude Code sessions by scanning JSONL conversation
// files in ~/.claude/projects/*/. Session files in ~/.claude/sessions/ are used
// as a secondary source for PID and startedAt enrichment.
type claudeScanner struct {
	// homeDir is the user's home directory, injected for testability.
	homeDir string
}

// newClaudeScanner creates a claudeScanner rooted at the given home directory.
func newClaudeScanner(homeDir string) *claudeScanner {
	return &claudeScanner{homeDir: homeDir}
}

// sessionFileData holds the fields parsed from a session JSON file.
type sessionFileData struct {
	PID       int
	Cwd       string
	StartedAt time.Time
}

// buildSessionIndex reads all ~/.claude/sessions/*.json files and returns
// a map from sessionId to session file data.
func (c *claudeScanner) buildSessionIndex() map[string]sessionFileData {
	sessDir := filepath.Join(c.homeDir, ".claude", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return nil
	}

	index := make(map[string]sessionFileData, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessDir, entry.Name()))
		if err != nil {
			continue
		}
		var sf struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			StartedAt int64  `json:"startedAt"`
		}
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}
		index[sf.SessionID] = sessionFileData{
			PID:       sf.PID,
			Cwd:       sf.Cwd,
			StartedAt: time.UnixMilli(sf.StartedAt),
		}
	}
	return index
}

// scan discovers all Claude Code sessions by scanning JSONL files across
// all project directories, enriching with PID data from session files.
func (c *claudeScanner) scan(ctx context.Context) ([]model.SessionInfo, error) {
	sessionIndex := c.buildSessionIndex()

	projectsDir := filepath.Join(c.homeDir, ".claude", "projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []model.SessionInfo
	for _, projEntry := range projEntries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !projEntry.IsDir() {
			continue
		}

		projPath := filepath.Join(projectsDir, projEntry.Name())
		jsonlFiles, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}

		for _, jf := range jsonlFiles {
			if jf.IsDir() || !strings.HasSuffix(jf.Name(), ".jsonl") {
				continue
			}

			sessionID := strings.TrimSuffix(jf.Name(), ".jsonl")
			jsonlPath := filepath.Join(projPath, jf.Name())

			info := model.SessionInfo{
				ID:       sessionID,
				Provider: model.ProviderClaude,
			}

			// Enrich from session file if available.
			if sf, ok := sessionIndex[sessionID]; ok {
				info.PID = sf.PID
				info.StartedAt = sf.StartedAt
				info.ProjectDir = sf.Cwd
			}

			// Parse JSONL for message stats, tokens, title, cwd.
			lastRole := c.parseConversation(jsonlPath, &info)

			// Count subagents.
			subagentDir := filepath.Join(projPath, sessionID, "subagents")
			if entries, err := os.ReadDir(subagentDir); err == nil {
				info.SubagentCount = len(entries)
			}

			// Get JSONL file modification time for status detection.
			var jsonlMtime time.Time
			if fi, err := os.Stat(jsonlPath); err == nil {
				jsonlMtime = fi.ModTime()
			}

			info.Status = c.determineStatus(info.PID, jsonlMtime, lastRole)

			sessions = append(sessions, info)
		}
	}

	return sessions, nil
}

// parseConversation reads the tail of a JSONL conversation file and populates
// the SessionInfo with message counts, token usage, title, cwd, and last active time.
// Returns the role of the last successfully parsed message.
func (c *claudeScanner) parseConversation(path string, info *model.SessionInfo) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	lines := readTailLines(f, tailLines)

	var lastRole string
	for _, line := range lines {
		var msg struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Slug      string    `json:"slug,omitempty"`
			Cwd       string    `json:"cwd,omitempty"`
			Message   *struct {
				Role  string `json:"role"`
				Usage *struct {
					InputTokens              int64 `json:"input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				} `json:"usage,omitempty"`
			} `json:"message,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		info.MessageCount++

		if msg.Message != nil {
			lastRole = msg.Message.Role
		}

		if !msg.Timestamp.IsZero() {
			info.LastActive = msg.Timestamp
			// Use earliest timestamp as StartedAt if not set from session file.
			if info.StartedAt.IsZero() {
				info.StartedAt = msg.Timestamp
			}
		}
		if msg.Slug != "" {
			info.Title = msg.Slug
		}
		// Extract cwd from user messages if not already set from session file.
		if info.ProjectDir == "" && msg.Cwd != "" {
			info.ProjectDir = msg.Cwd
		}
		if msg.Message != nil && msg.Message.Usage != nil {
			u := msg.Message.Usage
			info.InputTokens += u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			info.OutputTokens += u.OutputTokens
		}
	}

	return lastRole
}

// readTailLines reads the last n lines from a reader.
func readTailLines(r io.Reader, n int) []string {
	scanner := bufio.NewScanner(r)
	var ring []string
	for scanner.Scan() {
		text := scanner.Text()
		if text == "" {
			continue
		}
		ring = append(ring, text)
	}

	if len(ring) <= n {
		return ring
	}
	return ring[len(ring)-n:]
}

// determineStatus applies the status rules using JSONL file mtime and PID:
//  1. PID known and dead → finished
//  2. JSONL file modified within 2 min → active
//  3. JSONL mtime is zero (no file) + PID alive → active (brand new)
//  4. JSONL mtime is zero + no PID → finished
//  5. Stale JSONL + no PID → finished (e.g. claude -p that completed)
//  6. Stale JSONL + PID alive + last role "assistant" → waiting
//  7. Stale JSONL + PID alive → idle
func (c *claudeScanner) determineStatus(pid int, jsonlMtime time.Time, lastRole string) model.Status {
	if pid > 0 && !isProcessAlive(pid) {
		return model.StatusFinished
	}

	if !jsonlMtime.IsZero() && time.Since(jsonlMtime) < 2*time.Minute {
		return model.StatusActive
	}

	if jsonlMtime.IsZero() {
		if pid > 0 && isProcessAlive(pid) {
			return model.StatusActive
		}
		return model.StatusFinished
	}

	// Stale mtime with no known PID — assume finished.
	if pid == 0 {
		return model.StatusFinished
	}

	if lastRole == "assistant" {
		return model.StatusWaiting
	}

	return model.StatusIdle
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
