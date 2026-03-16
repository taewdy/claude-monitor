# claude-monitor

Monitors AI coding assistant sessions (Claude Code, OpenAI Codex CLI, GitHub Copilot) with a web dashboard and notifications.

## Overview

- Polls local session data from Claude Code, OpenAI Codex CLI, and GitHub Copilot
- Tracks status (active / idle / waiting / finished), token usage, project directories, and git branches
- Web dashboard on localhost
- Alerts via Slack, Teams, or macOS desktop notifications when sessions exit active state

## Usage

**Build:**

```
go build ./cmd/claude-monitor
```

**Run (basic — dashboard only):**

```
./claude-monitor
```

Opens dashboard at http://localhost:8555

**With desktop notifications:**

```
./claude-monitor --desktop-notify
```

**With Slack:**

```
./claude-monitor --slack-webhook https://hooks.slack.com/services/...
```

**With Teams:**

```
./claude-monitor --teams-webhook https://outlook.office.com/webhook/...
```

**All flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8555` | HTTP listen address |
| `--poll` | `5s` | Polling interval |
| `--desktop-notify` | `false` | Enable macOS desktop notifications |
| `--notify-sound` | `default` | Notification sound name |
| `--slack-webhook` | | Slack incoming webhook URL (or `SLACK_WEBHOOK_URL` env) |
| `--teams-webhook` | | Microsoft Teams incoming webhook URL (or `TEAMS_WEBHOOK_URL` env) |

**API:**

- `GET /api/sessions` — JSON array of all sessions
- `GET /api/sessions?days=7` — sessions from last 7 days (`days=-1` for all)
