// Package scanner provides session discovery for AI coding assistants.
package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	encoded := strings.ReplaceAll(sf.Cwd, "/", "-")
	jsonlPath := filepath.Join(c.homeDir, ".claude", "projects", encoded, sf.SessionID+".jsonl")
	lastRole := c.parseConversation(jsonlPath, &info)

	// Count subagents.
	subagentDir := filepath.Join(c.homeDir, ".claude", "projects", encoded, sf.SessionID, "subagents")
	if entries, err := os.ReadDir(subagentDir); err == nil {
		info.SubagentCount = len(entries)
	}

	// Get JSONL file modification time for freshness-based status detection.
	var jsonlMtime time.Time
	if fi, err := os.Stat(jsonlPath); err == nil {
		jsonlMtime = fi.ModTime()
	}

	// Determine status using file mtime (not content timestamps).
	info.Status = c.determineStatus(sf.PID, jsonlMtime, lastRole)

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
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Slug      string    `json:"slug,omitempty"`
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
		}
		if msg.Slug != "" {
			info.Title = msg.Slug
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

// determineStatus applies the status rules using JSONL file mtime as the
// primary freshness signal (cheap os.Stat before expensive ps shell-out):
//  1. PID dead → finished
//  2. PID alive + JSONL file modified within 2 min → active
//  3. PID alive + CPU > 2% → active (catches long tool runs)
//  4. PID alive + mtime is zero (no JSONL file) → active (brand new session)
//  5. PID alive + last role "assistant" → waiting
//  6. default → idle
func (c *claudeScanner) determineStatus(pid int, jsonlMtime time.Time, lastRole string) model.Status {
	if !isProcessAlive(pid) {
		return model.StatusFinished
	}

	if !jsonlMtime.IsZero() && time.Since(jsonlMtime) < 2*time.Minute {
		return model.StatusActive
	}

	if isProcessActive(pid) {
		return model.StatusActive
	}

	if jsonlMtime.IsZero() {
		return model.StatusActive
	}

	if lastRole == "assistant" {
		return model.StatusWaiting
	}

	return model.StatusIdle
}

// isProcessActive checks if a process is actively using CPU by shelling out to
// ps and checking if CPU usage exceeds 2.0%. Returns false on any error.
// Declared as a variable so tests can override it.
var isProcessActive = defaultIsProcessActive

func defaultIsProcessActive(pid int) bool {
	out, err := exec.Command("ps", "-o", "pcpu=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	cpu, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return false
	}
	return cpu > 2.0
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
