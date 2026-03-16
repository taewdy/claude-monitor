package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNoopNotifier_Notify(t *testing.T) {
	n := &NoopNotifier{}
	err := n.Notify(context.Background(), StatusChange{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

