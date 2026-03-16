// Package main is the entrypoint for claude-monitor.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/universe/claude-monitor/internal/monitor"
	"github.com/universe/claude-monitor/internal/notify"
	"github.com/universe/claude-monitor/internal/scanner"
	"github.com/universe/claude-monitor/internal/server"
)

func main() {
	addr := flag.String("addr", ":8555", "HTTP listen address")
	slackWebhook := flag.String("slack-webhook", "", "Slack incoming webhook URL")
	teamsWebhook := flag.String("teams-webhook", "", "Microsoft Teams incoming webhook URL")
	desktopNotify := flag.Bool("desktop-notify", false, "enable macOS desktop notifications")
	notifySound := flag.String("notify-sound", "default", "sound name for desktop notifications")
	poll := flag.Duration("poll", 5*time.Second, "polling interval")
	flag.Parse()

	// Resolve webhook URLs: CLI flag takes precedence over env var.
	slackURL := *slackWebhook
	if slackURL == "" {
		slackURL = os.Getenv("SLACK_WEBHOOK_URL")
	}
	teamsURL := *teamsWebhook
	if teamsURL == "" {
		teamsURL = os.Getenv("TEAMS_WEBHOOK_URL")
	}

	var notifiers []notify.Notifier
	if slackURL != "" {
		notifiers = append(notifiers, notify.NewSlackNotifier(slackURL))
	}
	if teamsURL != "" {
		notifiers = append(notifiers, notify.NewTeamsNotifier(teamsURL))
	}
	if *desktopNotify {
		notifiers = append(notifiers, notify.NewDesktopNotifier(*notifySound))
	}

	var notifier notify.Notifier
	switch len(notifiers) {
	case 0:
		notifier = &notify.NoopNotifier{}
	case 1:
		notifier = notifiers[0]
	default:
		notifier = notify.NewMultiNotifier(notifiers...)
	}

	sc := scanner.New()
	mon := monitor.New(sc, notifier, *poll)

	ctx := context.Background()
	mon.Start(ctx)
	defer mon.Stop()

	srv := server.New(mon)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	log.Printf("claude-monitor listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
