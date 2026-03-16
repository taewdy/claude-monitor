// Package notify provides status change notifications for AI coding sessions.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/universe/claude-monitor/internal/model"
)

// StatusChange describes a session's transition from one status to another.
type StatusChange struct {
	Session   model.SessionInfo
	OldStatus model.Status
	NewStatus model.Status
}

// Notifier sends notifications when a session's status changes.
type Notifier interface {
	Notify(ctx context.Context, change StatusChange) error
}

// slackPayload is the JSON body sent to a Slack incoming webhook.
type slackPayload struct {
	Text string `json:"text"`
}

// SlackNotifier posts status-change messages to a Slack incoming webhook.
type SlackNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewSlackNotifier creates a SlackNotifier that POSTs to the given webhook URL.
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{},
	}
}

// Notify sends a status-change message to Slack.
func (s *SlackNotifier) Notify(ctx context.Context, change StatusChange) error {
	label := change.Session.Title
	if label == "" {
		label = change.Session.ID
	}

	text := fmt.Sprintf(
		"%s session %q in %s: %s → %s",
		string(change.Session.Provider),
		label,
		change.Session.ProjectDir,
		string(change.OldStatus),
		string(change.NewStatus),
	)

	payload, err := json.Marshal(slackPayload{Text: text})
	if err != nil {
		return fmt.Errorf("marshalling slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// NoopNotifier discards all notifications.
type NoopNotifier struct{}

// Notify does nothing and always returns nil.
func (n *NoopNotifier) Notify(_ context.Context, _ StatusChange) error {
	return nil
}
