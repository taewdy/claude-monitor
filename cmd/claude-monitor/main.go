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
	poll := flag.Duration("poll", 5*time.Second, "polling interval")
	flag.Parse()

	// Resolve webhook URL: CLI flag takes precedence over env var.
	webhookURL := *slackWebhook
	if webhookURL == "" {
		webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	}

	var notifier notify.Notifier
	if webhookURL != "" {
		notifier = notify.NewSlackNotifier(webhookURL)
	} else {
		notifier = &notify.NoopNotifier{}
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
