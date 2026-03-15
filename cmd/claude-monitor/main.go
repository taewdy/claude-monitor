// Package main is the entrypoint for claude-monitor.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/universe/claude-monitor/internal/scanner"
	"github.com/universe/claude-monitor/internal/server"
)

func main() {
	addr := flag.String("addr", ":8555", "HTTP listen address")
	flag.Parse()

	sc := scanner.New()
	srv := server.New(sc)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	log.Printf("claude-monitor listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
