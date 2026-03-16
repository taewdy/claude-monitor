//go:generate mockgen -destination=mocks/mock_scanner.go -package=mocks github.com/universe/claude-monitor/internal/server SessionScanner

// Package server provides the HTTP handlers for the claude-monitor API and dashboard.
package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/universe/claude-monitor/internal/model"
)

//go:embed dashboard.html
var dashboardHTML []byte

// SessionScanner is the interface used by the server to retrieve sessions.
type SessionScanner interface {
	Scan(ctx context.Context) ([]model.SessionInfo, error)
}

// Server handles HTTP requests for the dashboard and API.
type Server struct {
	scanner SessionScanner
}

// New creates a new Server with the given scanner.
func New(scanner SessionScanner) *Server {
	return &Server{scanner: scanner}
}

// RegisterRoutes registers all HTTP routes on the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(dashboardHTML); err != nil {
		log.Printf("failed to write dashboard: %v", err)
	}
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.scanner.Scan(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("scan error: %v", err), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []model.SessionInfo{}
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(sessions); err != nil {
		http.Error(w, fmt.Sprintf("encode error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}
