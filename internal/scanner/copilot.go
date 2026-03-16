// Package scanner provides session discovery for AI coding assistants.
package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/universe/claude-monitor/internal/model"
)

// copilotScanner discovers GitHub Copilot sessions (both VSCode agent mode
// and standalone CLI) by reading workspace.yaml, events.jsonl, and lock
// files from ~/.copilot/.
type copilotScanner struct {
	// homeDir is the user's home directory, injected for testability.
	homeDir string
}

// newCopilotScanner creates a copilotScanner rooted at the given home directory.
func newCopilotScanner(homeDir string) *copilotScanner {
	return &copilotScanner{homeDir: homeDir}
}

// scan discovers all GitHub Copilot sessions and returns their info.
func (c *copilotScanner) scan(ctx context.Context) ([]model.SessionInfo, error) {
	sessionStateDir := filepath.Join(c.homeDir, ".copilot", "session-state")
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading copilot session-state dir: %w", err)
	}

	var sessions []model.SessionInfo

	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !entry.IsDir() {
			continue
		}

		sid := entry.Name()
		info, err := c.parseSession(sid)
		if err != nil {
			continue // skip sessions with unparseable data
		}
		if info == nil {
			continue
		}

		sessions = append(sessions, *info)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	return sessions, nil
}

// parseSession reads workspace.yaml, events.jsonl, and IDE lock file for a session.
func (c *copilotScanner) parseSession(sid string) (*model.SessionInfo, error) {
	sessionDir := filepath.Join(c.homeDir, ".copilot", "session-state", sid)

	// Parse workspace.yaml
	ws, err := c.parseWorkspaceYAML(filepath.Join(sessionDir, "workspace.yaml"))
	if err != nil {
		return nil, fmt.Errorf("parsing workspace.yaml: %w", err)
	}

	// Parse events.jsonl
	events := c.parseEventsJSONL(filepath.Join(sessionDir, "events.jsonl"))

	var (
		lastEventType string
		lastEventTime time.Time
		hasShutdown   bool
	)

	for _, e := range events {
		ts := time.Unix(e.Timestamp, 0).UTC()
		if ts.After(lastEventTime) || lastEventTime.IsZero() {
			lastEventTime = ts
			lastEventType = e.Type
		}
		if e.Type == "session.shutdown" {
			hasShutdown = true
		}
	}

	// Determine last active time
	lastActive := ws.updatedAt
	if !lastEventTime.IsZero() {
		lastActive = lastEventTime
	}

	// Determine status
	status := c.determineStatus(sid, hasShutdown, lastEventType, lastEventTime, len(events), ws.updatedAt)

	// Copilot CLI does not expose token usage in its event stream,
	// so InputTokens and OutputTokens are left at their zero values.
	info := &model.SessionInfo{
		ID:         ws.id,
		Provider:   model.ProviderCopilot,
		Status:     status,
		Title:      ws.summary,
		ProjectDir: ws.cwd,
		GitBranch:  ws.gitBranch,
		StartedAt:  ws.createdAt,
		LastActive: lastActive,
	}

	return info, nil
}

// determineStatus resolves the session status based on events and lock files.
// It handles both VSCode agent sessions (with events.jsonl) and CLI sessions
// (which may lack events but use inuse.*.lock files).
func (c *copilotScanner) determineStatus(sid string, hasShutdown bool, lastEventType string, lastEventTime time.Time, eventCount int, wsUpdatedAt time.Time) model.Status {
	if hasShutdown {
		return model.StatusFinished
	}

	// For sessions without events (typically CLI sessions), check lock files
	// for liveness. If the workspace hasn't been updated in 10 minutes,
	// treat it as idle even if the process lingers (daemon not cleaned up).
	if eventCount == 0 {
		if c.isSessionLockAlive(sid) || c.isIDELockAlive(sid) {
			if time.Since(wsUpdatedAt) >= 10*time.Minute {
				return model.StatusIdle
			}
			return model.StatusActive
		}
		return model.StatusFinished
	}

	if lastEventType == "assistant.turn_end" {
		return model.StatusWaiting
	}

	// Check lock files (both IDE lock and in-session lock)
	if c.isIDELockAlive(sid) || c.isSessionLockAlive(sid) {
		return model.StatusActive
	}

	// Time-based determination
	elapsed := time.Since(lastEventTime)
	if elapsed >= 60*time.Second {
		return model.StatusIdle
	}

	return model.StatusActive
}

// isIDELockAlive checks if an IDE lock file exists and its PID is alive.
func (c *copilotScanner) isIDELockAlive(sid string) bool {
	lockPath := filepath.Join(c.homeDir, ".copilot", "ide", sid+".lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return false
	}

	var lock struct {
		PID     int    `json:"pid"`
		IDEName string `json:"ideName"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return false
	}

	return isProcessAlive(lock.PID)
}

// isSessionLockAlive checks for an inuse.{PID}.lock file inside the session
// directory (used by the Copilot CLI) and returns true if the PID is alive.
func (c *copilotScanner) isSessionLockAlive(sid string) bool {
	sessionDir := filepath.Join(c.homeDir, ".copilot", "session-state", sid)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "inuse.") && strings.HasSuffix(name, ".lock") {
			pidStr := strings.TrimSuffix(strings.TrimPrefix(name, "inuse."), ".lock")
			pid, err := strconv.Atoi(pidStr)
			if err == nil && pid > 0 && isProcessAlive(pid) {
				return true
			}
		}
	}
	return false
}

// copilotWorkspace holds parsed fields from workspace.yaml.
type copilotWorkspace struct {
	id        string
	cwd       string
	gitBranch string
	gitRemote string
	createdAt time.Time
	updatedAt time.Time
	summary   string
}

// parseWorkspaceYAML manually parses a simple workspace.yaml without external deps.
func (c *copilotScanner) parseWorkspaceYAML(path string) (*copilotWorkspace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading workspace.yaml: %w", err)
	}

	ws := &copilotWorkspace{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := parseYAMLLine(line)
		if !ok {
			continue
		}
		switch key {
		case "id":
			ws.id = value
		case "cwd":
			ws.cwd = value
		case "branch", "git_branch":
			ws.gitBranch = value
		case "repository", "git_remote":
			ws.gitRemote = value
		case "created_at":
			ws.createdAt, _ = time.Parse(time.RFC3339, value)
		case "updated_at":
			ws.updatedAt, _ = time.Parse(time.RFC3339, value)
		case "summary":
			ws.summary = value
		}
	}

	if ws.id == "" {
		return nil, fmt.Errorf("workspace.yaml missing id field")
	}

	return ws, nil
}

// parseYAMLLine extracts key and value from a simple "key: value" YAML line.
func parseYAMLLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

// copilotEventParsed holds parsed fields from a single events.jsonl line.
type copilotEventParsed struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

// parseEventsJSONL reads and parses events from an events.jsonl file, skipping bad lines.
func (c *copilotScanner) parseEventsJSONL(path string) []copilotEventParsed {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var events []copilotEventParsed
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e copilotEventParsed
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines
		}
		events = append(events, e)
	}

	return events
}
