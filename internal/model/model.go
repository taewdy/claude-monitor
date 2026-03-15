// Package model defines the shared domain types for claude-monitor.
package model

import "time"

// Provider represents the AI coding assistant that owns a session.
type Provider string

const (
	// ProviderClaude represents Anthropic Claude Code sessions.
	ProviderClaude Provider = "claude"
	// ProviderCodex represents OpenAI Codex CLI sessions.
	ProviderCodex Provider = "codex"
	// ProviderCopilot represents GitHub Copilot CLI sessions.
	ProviderCopilot Provider = "copilot"
)

// Status represents the current state of a coding session.
type Status string

const (
	// StatusActive indicates the session is actively processing.
	StatusActive Status = "active"
	// StatusIdle indicates the session is alive but not recently active.
	StatusIdle Status = "idle"
	// StatusWaiting indicates the session is waiting for user input.
	StatusWaiting Status = "waiting"
	// StatusFinished indicates the session has ended.
	StatusFinished Status = "finished"
)

// SessionInfo holds the unified data for a single coding assistant session
// across all supported providers.
type SessionInfo struct {
	// ID is the unique session identifier.
	ID string `json:"id"`
	// Provider identifies which AI assistant owns this session.
	Provider Provider `json:"provider"`
	// Status is the current session state.
	Status Status `json:"status"`
	// Title is the session title or slug.
	Title string `json:"title"`
	// ProjectDir is the working directory of the session.
	ProjectDir string `json:"projectDir"`
	// GitBranch is the active git branch, if known.
	GitBranch string `json:"gitBranch,omitempty"`
	// StartedAt is when the session began.
	StartedAt time.Time `json:"startedAt"`
	// LastActive is the timestamp of the most recent activity.
	LastActive time.Time `json:"lastActive"`
	// InputTokens is the cumulative input token count.
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens is the cumulative output token count.
	OutputTokens int64 `json:"outputTokens"`
	// MessageCount is the total number of messages in the session.
	MessageCount int `json:"messageCount"`
	// SubagentCount is the number of active subagents (Claude-specific).
	SubagentCount int `json:"subagentCount,omitempty"`
	// PID is the process ID of the session, if applicable.
	PID int `json:"pid,omitempty"`
}
