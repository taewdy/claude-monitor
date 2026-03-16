// Package scanner provides session discovery for AI coding assistants.
package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

const tailLines = 50

// claudeScanner discovers Claude Code sessions by reading session files
// from ~/.claude/sessions/ and correlating them with conversation JSONL files.
type claudeScanner struct {
	// homeDir is the user's home directory, injected for testability.
	homeDir string
}

// newClaudeScanner creates a claudeScanner rooted at the given home directory.
func newClaudeScanner(homeDir string) *claudeScanner {
	return &claudeScanner{homeDir: homeDir}
}

// scan discovers all Claude Code sessions and returns their info.
func (c *claudeScanner) scan(ctx context.Context) ([]model.SessionInfo, error) {
	sessDir := filepath.Join(c.homeDir, ".claude", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []model.SessionInfo
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, ok := c.processSessionFile(filepath.Join(sessDir, entry.Name()))
		if !ok {
			continue
		}
		sessions = append(sessions, info)
	}

	return sessions, nil
}

// processSessionFile reads and parses a single session JSON file, correlates
// it with its conversation JSONL, and returns a populated SessionInfo.
// Returns false if the file should be skipped.
func (c *claudeScanner) processSessionFile(path string) (model.SessionInfo, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.SessionInfo{}, false
	}

	var sf struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		StartedAt int64  `json:"startedAt"`
	}
	if err := json.Unmarshal(data, &sf); err != nil {
		return model.SessionInfo{}, false
	}

	startedAt := time.UnixMilli(sf.StartedAt)

	info := model.SessionInfo{
		ID:         sf.SessionID,
		Provider:   model.ProviderClaude,
		ProjectDir: sf.Cwd,
		StartedAt:  startedAt,
		PID:        sf.PID,
	}

	// Parse conversation JSONL for message stats.
	encoded := url.PathEscape(sf.Cwd)
	jsonlPath := filepath.Join(c.homeDir, ".claude", "projects", encoded, sf.SessionID+".jsonl")
	lastRole := c.parseConversation(jsonlPath, &info)

	// Count subagents.
	subagentDir := filepath.Join(c.homeDir, ".claude", "projects", encoded, sf.SessionID, "subagents")
	if entries, err := os.ReadDir(subagentDir); err == nil {
		info.SubagentCount = len(entries)
	}

	// Determine status.
	info.Status = c.determineStatus(sf.PID, info.LastActive, lastRole)

	return info, true
}

// parseConversation reads the tail of a JSONL conversation file and populates
// the SessionInfo with message counts, token usage, title, and last active time.
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
			Role      string    `json:"role"`
			Timestamp time.Time `json:"timestamp"`
			Slug      string    `json:"slug,omitempty"`
			Usage     *struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		info.MessageCount++
		lastRole = msg.Role

		if !msg.Timestamp.IsZero() {
			info.LastActive = msg.Timestamp
		}
		if msg.Slug != "" {
			info.Title = msg.Slug
		}
		if msg.Usage != nil {
			info.InputTokens += msg.Usage.InputTokens
			info.OutputTokens += msg.Usage.OutputTokens
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

// determineStatus applies the status rules:
//   - PID dead → finished
//   - PID alive + last msg is assistant role → waiting
//   - PID alive + last msg < 60s → active
//   - PID alive + last msg > 60s → idle
func (c *claudeScanner) determineStatus(pid int, lastActive time.Time, lastRole string) model.Status {
	if !isProcessAlive(pid) {
		return model.StatusFinished
	}

	if lastRole == "assistant" {
		return model.StatusWaiting
	}

	if !lastActive.IsZero() && time.Since(lastActive) < 60*time.Second {
		return model.StatusActive
	}

	return model.StatusIdle
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
