package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestProviderConstants(t *testing.T) {
	tests := map[string]struct {
		provider Provider
		want     string
	}{
		"claude":  {provider: ProviderClaude, want: "claude"},
		"codex":   {provider: ProviderCodex, want: "codex"},
		"copilot": {provider: ProviderCopilot, want: "copilot"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if string(tt.provider) != tt.want {
				t.Errorf("expected %q, got %q", tt.want, string(tt.provider))
			}
		})
	}
}

func TestStatusConstants(t *testing.T) {
	tests := map[string]struct {
		status Status
		want   string
	}{
		"active":   {status: StatusActive, want: "active"},
		"idle":     {status: StatusIdle, want: "idle"},
		"waiting":  {status: StatusWaiting, want: "waiting"},
		"finished": {status: StatusFinished, want: "finished"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("expected %q, got %q", tt.want, string(tt.status))
			}
		})
	}
}

func TestSessionInfo_JSONRoundTrip(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	earlier := now.Add(-30 * time.Minute)

	tests := map[string]struct {
		input SessionInfo
		want  SessionInfo
	}{
		"full_session": {
			input: SessionInfo{
				ID:            "sess-001",
				Provider:      ProviderClaude,
				Status:        StatusActive,
				Title:         "fix auth bug",
				ProjectDir:    "/home/user/project",
				GitBranch:     "feat/auth",
				StartedAt:     earlier,
				LastActive:    now,
				InputTokens:   1500,
				OutputTokens:  3200,
				MessageCount:  12,
				SubagentCount: 2,
				PID:           42,
			},
			want: SessionInfo{
				ID:            "sess-001",
				Provider:      ProviderClaude,
				Status:        StatusActive,
				Title:         "fix auth bug",
				ProjectDir:    "/home/user/project",
				GitBranch:     "feat/auth",
				StartedAt:     earlier,
				LastActive:    now,
				InputTokens:   1500,
				OutputTokens:  3200,
				MessageCount:  12,
				SubagentCount: 2,
				PID:           42,
			},
		},
		"zero_optional_fields_omitted": {
			input: SessionInfo{
				ID:         "sess-002",
				Provider:   ProviderCodex,
				Status:     StatusIdle,
				Title:      "refactor",
				ProjectDir: "/tmp/proj",
				StartedAt:  earlier,
				LastActive: now,
			},
			want: SessionInfo{
				ID:         "sess-002",
				Provider:   ProviderCodex,
				Status:     StatusIdle,
				Title:      "refactor",
				ProjectDir: "/tmp/proj",
				StartedAt:  earlier,
				LastActive: now,
			},
		},
		"empty_session": {
			input: SessionInfo{},
			want:  SessionInfo{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}

			var got SessionInfo
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestSessionInfo_JSONFieldNames(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	session := SessionInfo{
		ID:            "s1",
		Provider:      ProviderClaude,
		Status:        StatusActive,
		Title:         "test",
		ProjectDir:    "/proj",
		GitBranch:     "main",
		StartedAt:     now,
		LastActive:    now,
		InputTokens:   100,
		OutputTokens:  200,
		MessageCount:  5,
		SubagentCount: 1,
		PID:           99,
	}

	data, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	requiredKeys := []string{
		"id", "provider", "status", "title", "projectDir",
		"gitBranch", "startedAt", "lastActive",
		"inputTokens", "outputTokens", "messageCount",
		"subagentCount", "pid",
	}

	for _, key := range requiredKeys {
		t.Run(key, func(t *testing.T) {
			if _, ok := raw[key]; !ok {
				t.Errorf("expected JSON key %q not found", key)
			}
		})
	}
}

func TestSessionInfo_OmitEmptyFields(t *testing.T) {
	session := SessionInfo{
		ID:         "s1",
		Provider:   ProviderClaude,
		Status:     StatusActive,
		Title:      "test",
		ProjectDir: "/proj",
	}

	data, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	omittedKeys := []string{"gitBranch", "subagentCount", "pid"}

	for _, key := range omittedKeys {
		t.Run(key+"_omitted_when_zero", func(t *testing.T) {
			if _, ok := raw[key]; ok {
				t.Errorf("expected JSON key %q to be omitted when zero value, but it was present", key)
			}
		})
	}
}
