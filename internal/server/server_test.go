package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/universe/claude-monitor/internal/model"
	"github.com/universe/claude-monitor/internal/server/mocks"
	"go.uber.org/mock/gomock"
)

// doRequest is a test helper that sets up the server with a mock scanner,
// sends an HTTP request, and returns the response recorder.
func doRequest(t *testing.T, method, path string, mockSetup func(*mocks.MockSessionScanner)) *httptest.ResponseRecorder {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockScanner := mocks.NewMockSessionScanner(ctrl)
	if mockSetup != nil {
		mockSetup(mockScanner)
	}

	srv := New(mockScanner)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestNew(t *testing.T) {
	tests := map[string]struct {
		scanner SessionScanner
	}{
		"with_mock_scanner": {
			scanner: mocks.NewMockSessionScanner(gomock.NewController(t)),
		},
		"with_nil_scanner": {
			scanner: nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := New(tt.scanner)
			if srv == nil {
				t.Fatal("New() returned nil")
			}
		})
	}
}

func TestServer_RegisterRoutes(t *testing.T) {
	tests := map[string]struct {
		method     string
		path       string
		wantStatus int
	}{
		"dashboard_root": {
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusOK,
		},
		"sessions_api": {
			method:     http.MethodGet,
			path:       "/api/sessions?days=-1",
			wantStatus: http.StatusOK,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, tt.method, tt.path, func(s *mocks.MockSessionScanner) {
				s.EXPECT().Scan(gomock.Any()).Return(nil, nil).AnyTimes()
			})

			if rec.Code != tt.wantStatus {
				t.Errorf("status code: got %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestServer_HandleDashboard(t *testing.T) {
	rec := doRequest(t, http.MethodGet, "/", func(s *mocks.MockSessionScanner) {
		s.EXPECT().Scan(gomock.Any()).Return(nil, nil).AnyTimes()
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status code: got %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q, want %q", got, "text/html; charset=utf-8")
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body")
	}
}

func TestServer_HandleSessions(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	earlier := now.Add(-5 * time.Minute)

	scanErr := errors.New("scanner failed")

	type wants struct {
		statusCode  int
		contentType string
		sessions    []model.SessionInfo
		errContains string
	}

	tests := map[string]struct {
		mockSetup func(scanner *mocks.MockSessionScanner)
		wants     wants
	}{
		"returns_sessions_as_json": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
					{
						ID:            "s1",
						Provider:      model.ProviderClaude,
						Status:        model.StatusActive,
						Title:         "test session",
						ProjectDir:    "/proj",
						StartedAt:     now,
						LastActive:    now,
						InputTokens:   100,
						OutputTokens:  200,
						MessageCount:  5,
						SubagentCount: 2,
					},
				}, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions: []model.SessionInfo{
					{
						ID:            "s1",
						Provider:      model.ProviderClaude,
						Status:        model.StatusActive,
						Title:         "test session",
						ProjectDir:    "/proj",
						StartedAt:     now,
						LastActive:    now,
						InputTokens:   100,
						OutputTokens:  200,
						MessageCount:  5,
						SubagentCount: 2,
					},
				},
			},
		},
		"multiple_sessions_multiple_providers": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
					{
						ID:         "s1",
						Provider:   model.ProviderClaude,
						Status:     model.StatusActive,
						Title:      "claude session",
						ProjectDir: "/proj/a",
						GitBranch:  "feat/auth",
						StartedAt:  earlier,
						LastActive: now,
					},
					{
						ID:         "s2",
						Provider:   model.ProviderCodex,
						Status:     model.StatusIdle,
						Title:      "codex session",
						ProjectDir: "/proj/b",
						StartedAt:  earlier,
						LastActive: now,
					},
					{
						ID:         "s3",
						Provider:   model.ProviderCopilot,
						Status:     model.StatusWaiting,
						Title:      "copilot session",
						ProjectDir: "/proj/c",
						StartedAt:  earlier,
						LastActive: now,
					},
				}, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions: []model.SessionInfo{
					{ID: "s1", Provider: model.ProviderClaude, Status: model.StatusActive, Title: "claude session", ProjectDir: "/proj/a", GitBranch: "feat/auth", StartedAt: earlier, LastActive: now},
					{ID: "s2", Provider: model.ProviderCodex, Status: model.StatusIdle, Title: "codex session", ProjectDir: "/proj/b", StartedAt: earlier, LastActive: now},
					{ID: "s3", Provider: model.ProviderCopilot, Status: model.StatusWaiting, Title: "copilot session", ProjectDir: "/proj/c", StartedAt: earlier, LastActive: now},
				},
			},
		},
		"returns_empty_array_when_no_sessions": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{}, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions:    []model.SessionInfo{},
			},
		},
		"nil_scan_result_returns_empty_array": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return(nil, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions:    []model.SessionInfo{},
			},
		},
		"scan_error_returns_500": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return(nil, scanErr)
			},
			wants: wants{
				statusCode:  http.StatusInternalServerError,
				errContains: "scan error",
			},
		},
		"session_with_all_fields_populated": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
					{
						ID:            "full-session",
						Provider:      model.ProviderClaude,
						Status:        model.StatusActive,
						Title:         "full test",
						ProjectDir:    "/home/user/project",
						GitBranch:     "main",
						StartedAt:     earlier,
						LastActive:    now,
						InputTokens:   15000,
						OutputTokens:  32000,
						MessageCount:  42,
						SubagentCount: 3,
						PID:           9876,
					},
				}, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions: []model.SessionInfo{
					{
						ID:            "full-session",
						Provider:      model.ProviderClaude,
						Status:        model.StatusActive,
						Title:         "full test",
						ProjectDir:    "/home/user/project",
						GitBranch:     "main",
						StartedAt:     earlier,
						LastActive:    now,
						InputTokens:   15000,
						OutputTokens:  32000,
						MessageCount:  42,
						SubagentCount: 3,
						PID:           9876,
					},
				},
			},
		},
		"session_with_finished_status": {
			mockSetup: func(scanner *mocks.MockSessionScanner) {
				scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
					{
						ID:         "done-1",
						Provider:   model.ProviderCodex,
						Status:     model.StatusFinished,
						Title:      "completed task",
						ProjectDir: "/tmp",
						StartedAt:  earlier,
						LastActive: now,
					},
				}, nil)
			},
			wants: wants{
				statusCode:  http.StatusOK,
				contentType: "application/json",
				sessions: []model.SessionInfo{
					{
						ID:         "done-1",
						Provider:   model.ProviderCodex,
						Status:     model.StatusFinished,
						Title:      "completed task",
						ProjectDir: "/tmp",
						StartedAt:  earlier,
						LastActive: now,
					},
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, http.MethodGet, "/api/sessions?days=-1", tt.mockSetup)

			if rec.Code != tt.wants.statusCode {
				t.Errorf("status code: got %d, want %d", rec.Code, tt.wants.statusCode)
			}

			if tt.wants.errContains != "" {
				body := rec.Body.String()
				if !strings.Contains(body, tt.wants.errContains) {
					t.Errorf("expected body to contain %q, got %q", tt.wants.errContains, body)
				}
				return
			}

			if got := rec.Header().Get("Content-Type"); got != tt.wants.contentType {
				t.Errorf("Content-Type: got %q, want %q", got, tt.wants.contentType)
			}

			if tt.wants.sessions != nil {
				var got []model.SessionInfo
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}

				if !reflect.DeepEqual(got, tt.wants.sessions) {
					t.Errorf("sessions mismatch:\ngot:  %+v\nwant: %+v", got, tt.wants.sessions)
				}
			}
		})
	}
}

func TestServer_HandleSessions_ResponseFormat(t *testing.T) {
	now := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)

	rec := doRequest(t, http.MethodGet, "/api/sessions?days=-1", func(s *mocks.MockSessionScanner) {
		s.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
			{
				ID:            "sess-1",
				Provider:      model.ProviderClaude,
				Status:        model.StatusActive,
				Title:         "test session",
				ProjectDir:    "/proj",
				GitBranch:     "main",
				StartedAt:     now,
				LastActive:    now,
				InputTokens:   500,
				OutputTokens:  1000,
				MessageCount:  3,
				SubagentCount: 1,
				PID:           1234,
			},
		}, nil)
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify the JSON contains expected camelCase field names.
	var raw []map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(raw) != 1 {
		t.Fatalf("expected 1 session, got %d", len(raw))
	}

	expectedKeys := []string{
		"id", "provider", "status", "title", "projectDir",
		"gitBranch", "startedAt", "lastActive",
		"inputTokens", "outputTokens", "messageCount",
		"subagentCount", "pid",
	}

	for _, key := range expectedKeys {
		t.Run("json_field_"+key, func(t *testing.T) {
			if _, ok := raw[0][key]; !ok {
				t.Errorf("expected JSON key %q not found in response", key)
			}
		})
	}
}

func TestServer_HandleSessions_EmptyArrayNotNull(t *testing.T) {
	// When scanner returns nil, the JSON response should be [] not null.
	rec := doRequest(t, http.MethodGet, "/api/sessions?days=-1", func(s *mocks.MockSessionScanner) {
		s.EXPECT().Scan(gomock.Any()).Return(nil, nil)
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want %d", rec.Code, http.StatusOK)
	}

	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Errorf("expected empty JSON array '[]', got %q", body)
	}
}

func TestServer_HandleSessions_DaysFilter(t *testing.T) {
	// Fix "now" so tests are deterministic.
	fakeNow := time.Date(2026, 3, 17, 14, 0, 0, 0, time.Local)
	origNow := nowFunc
	nowFunc = func() time.Time { return fakeNow }
	t.Cleanup(func() { nowFunc = origNow })

	today := time.Date(2026, 3, 17, 9, 0, 0, 0, time.Local)
	yesterday := time.Date(2026, 3, 16, 15, 0, 0, 0, time.Local)
	threeDaysAgo := time.Date(2026, 3, 14, 12, 0, 0, 0, time.Local)

	allSessions := []model.SessionInfo{
		{ID: "today", Provider: model.ProviderClaude, Status: model.StatusActive, LastActive: today},
		{ID: "yesterday", Provider: model.ProviderClaude, Status: model.StatusIdle, LastActive: yesterday},
		{ID: "old", Provider: model.ProviderClaude, Status: model.StatusFinished, LastActive: threeDaysAgo},
		{ID: "brand-new", Provider: model.ProviderClaude, Status: model.StatusActive}, // zero LastActive
	}

	mockSetup := func(s *mocks.MockSessionScanner) {
		s.EXPECT().Scan(gomock.Any()).Return(allSessions, nil)
	}

	tests := map[string]struct {
		path    string
		wantIDs []string
	}{
		"default_no_param_today_only": {
			path:    "/api/sessions",
			wantIDs: []string{"today", "brand-new"},
		},
		"days_0_same_as_default": {
			path:    "/api/sessions?days=0",
			wantIDs: []string{"today", "brand-new"},
		},
		"days_1_today_and_yesterday": {
			path:    "/api/sessions?days=1",
			wantIDs: []string{"today", "yesterday", "brand-new"},
		},
		"days_7_last_week": {
			path:    "/api/sessions?days=7",
			wantIDs: []string{"today", "yesterday", "old", "brand-new"},
		},
		"days_negative_1_all_sessions": {
			path:    "/api/sessions?days=-1",
			wantIDs: []string{"today", "yesterday", "old", "brand-new"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, http.MethodGet, tt.path, mockSetup)

			if rec.Code != http.StatusOK {
				t.Fatalf("status code: got %d, want %d", rec.Code, http.StatusOK)
			}

			var got []model.SessionInfo
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			gotIDs := make([]string, len(got))
			for i, s := range got {
				gotIDs[i] = s.ID
			}

			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("session IDs mismatch:\ngot:  %v\nwant: %v", gotIDs, tt.wantIDs)
			}
		})
	}
}
