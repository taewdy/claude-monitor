// Package notify provides status change notifications for AI coding sessions.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"

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

// formatMessage builds the human-readable status-change text shared by all notifiers.
func formatMessage(change StatusChange) string {
	label := change.Session.Title
	if label == "" {
		label = change.Session.ID
	}
	return fmt.Sprintf(
		"%s session %q in %s: %s → %s",
		string(change.Session.Provider),
		label,
		change.Session.ProjectDir,
		string(change.OldStatus),
		string(change.NewStatus),
	)
}

// postJSON marshals payload as JSON and POSTs it to url. name is used in error messages.
func postJSON(ctx context.Context, client *http.Client, url string, payload any, name string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling %s payload: %w", name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating %s request: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s webhook: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s webhook returned status %d", name, resp.StatusCode)
	}

	return nil
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
	return postJSON(ctx, s.client, s.webhookURL, slackPayload{Text: formatMessage(change)}, "slack")
}

// teamsPayload is the JSON body sent to a Teams incoming webhook.
type teamsPayload struct {
	Type       string `json:"@type"`
	Summary    string `json:"summary"`
	ThemeColor string `json:"themeColor"`
	Text       string `json:"text"`
}

// TeamsNotifier posts status-change messages to a Microsoft Teams incoming webhook.
type TeamsNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewTeamsNotifier creates a TeamsNotifier that POSTs to the given webhook URL.
func NewTeamsNotifier(webhookURL string) *TeamsNotifier {
	return &TeamsNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{},
	}
}

// themeColor returns a hex color based on the new status.
func themeColor(status model.Status) string {
	switch status {
	case model.StatusActive:
		return "00CC00"
	case model.StatusIdle:
		return "FFAA00"
	case model.StatusWaiting:
		return "FF6600"
	case model.StatusFinished:
		return "888888"
	default:
		return "0076D7"
	}
}

// Notify sends a status-change message to Teams.
func (t *TeamsNotifier) Notify(ctx context.Context, change StatusChange) error {
	text := formatMessage(change)
	return postJSON(ctx, t.client, t.webhookURL, teamsPayload{
		Type:       "MessageCard",
		Summary:    text,
		ThemeColor: themeColor(change.NewStatus),
		Text:       text,
	}, "teams")
}

// MultiNotifier fans out notifications to multiple notifiers.
type MultiNotifier struct {
	notifiers []Notifier
}

// NewMultiNotifier creates a MultiNotifier that sends to all provided notifiers.
func NewMultiNotifier(notifiers ...Notifier) *MultiNotifier {
	return &MultiNotifier{notifiers: notifiers}
}

// Notify sends the status change to every wrapped notifier, collecting all errors.
// All notifiers are attempted even if some fail.
func (m *MultiNotifier) Notify(ctx context.Context, change StatusChange) error {
	var errs []error
	for _, n := range m.notifiers {
		if err := n.Notify(ctx, change); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DesktopNotifier sends macOS desktop notifications via osascript.
type DesktopNotifier struct {
	soundName string
}

// NewDesktopNotifier creates a DesktopNotifier that displays native macOS notifications.
// soundName controls the alert sound (e.g. "default", "Ping", "Basso").
func NewDesktopNotifier(soundName string) *DesktopNotifier {
	return &DesktopNotifier{soundName: soundName}
}

// buildScript returns the AppleScript expression for a desktop notification.
func (d *DesktopNotifier) buildScript(message string) string {
	return fmt.Sprintf(`display notification %q with title "Claude Monitor" sound name %q`, message, d.soundName)
}

// Notify displays a macOS desktop notification for the status change.
func (d *DesktopNotifier) Notify(ctx context.Context, change StatusChange) error {
	script := d.buildScript(formatMessage(change))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("executing osascript: %w", err)
	}
	return nil
}

// NoopNotifier discards all notifications.
type NoopNotifier struct{}

// Notify does nothing and always returns nil.
func (n *NoopNotifier) Notify(_ context.Context, _ StatusChange) error {
	return nil
}
