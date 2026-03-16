package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universe/claude-monitor/internal/model"
)

func TestSlackNotifier_Notify(t *testing.T) {
	var received slackPayload
	var contentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewSlackNotifier(srv.URL)
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "abc-123",
			Provider:   model.ProviderClaude,
			Title:      "refactor auth",
			ProjectDir: "/home/user/project",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusIdle,
	}

	err := notifier.Notify(context.Background(), change)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if contentType != "application/json" {
		t.Errorf("content type = %q, want %q", contentType, "application/json")
	}

	if received.Text == "" {
		t.Fatal("received empty text payload")
	}

	// Verify expected fields are present in the message.
	for _, want := range []string{"claude", "refactor auth", "/home/user/project", "active", "idle"} {
		if !strings.Contains(received.Text, want) {
			t.Errorf("payload text %q missing expected substring %q", received.Text, want)
		}
	}
}

func TestSlackNotifier_Notify_FallsBackToID(t *testing.T) {
	var received slackPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewSlackNotifier(srv.URL)
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "sess-456",
			Provider:   model.ProviderCodex,
			ProjectDir: "/tmp/proj",
		},
		OldStatus: model.StatusWaiting,
		NewStatus: model.StatusFinished,
	}

	err := notifier.Notify(context.Background(), change)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(received.Text, "sess-456") {
		t.Errorf("payload text %q should contain session ID when title is empty", received.Text)
	}
}

func TestSlackNotifier_Notify_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := NewSlackNotifier(srv.URL)
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "x",
			Provider:   model.ProviderClaude,
			ProjectDir: "/tmp",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusFinished,
	}

	err := notifier.Notify(context.Background(), change)
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
}

func TestSlackNotifier_Notify_ConnectionError(t *testing.T) {
	// Use a URL that will fail to connect.
	notifier := NewSlackNotifier("http://127.0.0.1:1")
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "x",
			Provider:   model.ProviderClaude,
			ProjectDir: "/tmp",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusFinished,
	}

	err := notifier.Notify(context.Background(), change)
	if err == nil {
		t.Fatal("expected error for connection failure, got nil")
	}
}

func TestTeamsNotifier_Notify(t *testing.T) {
	var received teamsPayload
	var contentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewTeamsNotifier(srv.URL)
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "abc-123",
			Provider:   model.ProviderClaude,
			Title:      "refactor auth",
			ProjectDir: "/home/user/project",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusIdle,
	}

	err := notifier.Notify(context.Background(), change)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if contentType != "application/json" {
		t.Errorf("content type = %q, want %q", contentType, "application/json")
	}

	if received.Type != "MessageCard" {
		t.Errorf("@type = %q, want %q", received.Type, "MessageCard")
	}

	if received.ThemeColor != "FFAA00" {
		t.Errorf("themeColor = %q, want %q for idle status", received.ThemeColor, "FFAA00")
	}

	if received.Summary == "" {
		t.Fatal("received empty summary")
	}

	for _, want := range []string{"claude", "refactor auth", "/home/user/project", "active", "idle"} {
		if !strings.Contains(received.Text, want) {
			t.Errorf("payload text %q missing expected substring %q", received.Text, want)
		}
	}
}

func TestTeamsNotifier_Notify_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := NewTeamsNotifier(srv.URL)
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "x",
			Provider:   model.ProviderClaude,
			ProjectDir: "/tmp",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusFinished,
	}

	err := notifier.Notify(context.Background(), change)
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
}

func TestMultiNotifier_FansOutToAll(t *testing.T) {
	var calls atomic.Int32

	// Create two test servers to act as notifiers.
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	multi := NewMultiNotifier(
		NewSlackNotifier(srv1.URL),
		NewTeamsNotifier(srv2.URL),
	)

	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "abc",
			Provider:   model.ProviderClaude,
			ProjectDir: "/tmp",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusIdle,
	}

	err := multi.Notify(context.Background(), change)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 notifier calls, got %d", got)
	}
}

func TestMultiNotifier_AttemptsAllNotifiers(t *testing.T) {
	var calls atomic.Int32

	// Both servers return errors.
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv2.Close()

	multi := NewMultiNotifier(
		NewSlackNotifier(srv1.URL),
		NewTeamsNotifier(srv2.URL),
	)

	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "x",
			Provider:   model.ProviderClaude,
			ProjectDir: "/tmp",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusIdle,
	}

	err := multi.Notify(context.Background(), change)
	if err == nil {
		t.Fatal("expected error from multi-notifier, got nil")
	}

	// Verify both notifiers were attempted despite errors.
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 notifier calls (best-effort), got %d", got)
	}

	// Verify both errors are present.
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention status 500", err.Error())
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error %q should mention status 502", err.Error())
	}
}

func TestDesktopNotifier_BuildScript(t *testing.T) {
	d := NewDesktopNotifier("default")
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "abc-123",
			Provider:   model.ProviderClaude,
			Title:      "refactor auth",
			ProjectDir: "/home/user/project",
		},
		OldStatus: model.StatusActive,
		NewStatus: model.StatusIdle,
	}

	script := d.buildScript(formatMessage(change))

	// Verify the script contains the expected osascript structure.
	if !strings.Contains(script, "display notification") {
		t.Errorf("script %q missing 'display notification'", script)
	}
	if !strings.Contains(script, `with title "Claude Monitor"`) {
		t.Errorf("script %q missing title", script)
	}
	if !strings.Contains(script, `sound name "default"`) {
		t.Errorf("script %q missing sound name", script)
	}

	// Verify the message content includes expected fields.
	for _, want := range []string{"claude", "refactor auth", "/home/user/project", "active", "idle"} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q missing expected substring %q", script, want)
		}
	}
}

func TestDesktopNotifier_BuildScript_CustomSound(t *testing.T) {
	d := NewDesktopNotifier("Ping")
	script := d.buildScript("test message")

	if !strings.Contains(script, `sound name "Ping"`) {
		t.Errorf("script %q missing custom sound name 'Ping'", script)
	}
}

func TestDesktopNotifier_BuildScript_FallsBackToID(t *testing.T) {
	d := NewDesktopNotifier("default")
	change := StatusChange{
		Session: model.SessionInfo{
			ID:         "sess-789",
			Provider:   model.ProviderCodex,
			ProjectDir: "/tmp/proj",
		},
		OldStatus: model.StatusWaiting,
		NewStatus: model.StatusFinished,
	}

	script := d.buildScript(formatMessage(change))

	if !strings.Contains(script, "sess-789") {
		t.Errorf("script %q should contain session ID when title is empty", script)
	}
}

func TestNoopNotifier_Notify(t *testing.T) {
	n := &NoopNotifier{}
	err := n.Notify(context.Background(), StatusChange{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

