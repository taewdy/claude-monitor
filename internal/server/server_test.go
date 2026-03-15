package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	ctrl := gomock.NewController(t)
	mockScanner := mocks.NewMockSessionScanner(ctrl)

	srv := New(mockScanner)
	if srv == nil {
		t.Fatal("New() returned nil")
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
			path:       "/api/sessions",
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

func TestServer_HandleSessions(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	type fields struct {
		mockOperations func(scanner *mocks.MockSessionScanner)
	}

	type wants struct {
		statusCode int
		sessions   []model.SessionInfo
		errBody    bool
	}

	tests := map[string]struct {
		fields fields
		wants  wants
	}{
		"returns_sessions_as_json": {
			fields: fields{
				mockOperations: func(scanner *mocks.MockSessionScanner) {
					scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{
						{
							ID:           "s1",
							Provider:     model.ProviderClaude,
							Status:       model.StatusActive,
							Title:        "test session",
							ProjectDir:   "/proj",
							StartedAt:    now,
							LastActive:   now,
							InputTokens:  100,
							OutputTokens: 200,
							MessageCount: 5,
						},
					}, nil)
				},
			},
			wants: wants{
				statusCode: http.StatusOK,
				sessions: []model.SessionInfo{
					{
						ID:           "s1",
						Provider:     model.ProviderClaude,
						Status:       model.StatusActive,
						Title:        "test session",
						ProjectDir:   "/proj",
						StartedAt:    now,
						LastActive:   now,
						InputTokens:  100,
						OutputTokens: 200,
						MessageCount: 5,
					},
				},
			},
		},
		"returns_empty_array_when_no_sessions": {
			fields: fields{
				mockOperations: func(scanner *mocks.MockSessionScanner) {
					scanner.EXPECT().Scan(gomock.Any()).Return([]model.SessionInfo{}, nil)
				},
			},
			wants: wants{
				statusCode: http.StatusOK,
				sessions:   []model.SessionInfo{},
			},
		},
		"returns_error_when_scan_fails": {
			fields: fields{
				mockOperations: func(scanner *mocks.MockSessionScanner) {
					scanner.EXPECT().Scan(gomock.Any()).Return(nil, errors.New("scan failed"))
				},
			},
			wants: wants{
				statusCode: http.StatusInternalServerError,
				errBody:    true,
			},
		},
		"returns_nil_scan_result_as_empty": {
			fields: fields{
				mockOperations: func(scanner *mocks.MockSessionScanner) {
					scanner.EXPECT().Scan(gomock.Any()).Return(nil, nil)
				},
			},
			wants: wants{
				statusCode: http.StatusOK,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, http.MethodGet, "/api/sessions", tt.fields.mockOperations)

			if rec.Code != tt.wants.statusCode {
				t.Errorf("status code: got %d, want %d", rec.Code, tt.wants.statusCode)
			}

			if tt.wants.errBody {
				if rec.Body.Len() == 0 {
					t.Error("expected error body, got empty response")
				}
				return
			}

			if tt.wants.sessions != nil {
				var got []model.SessionInfo
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}

				if len(got) != len(tt.wants.sessions) {
					t.Errorf("session count: got %d, want %d", len(got), len(tt.wants.sessions))
					return
				}

				for i, want := range tt.wants.sessions {
					if got[i].ID != want.ID {
						t.Errorf("session[%d].ID: got %q, want %q", i, got[i].ID, want.ID)
					}
					if got[i].Provider != want.Provider {
						t.Errorf("session[%d].Provider: got %q, want %q", i, got[i].Provider, want.Provider)
					}
					if got[i].Status != want.Status {
						t.Errorf("session[%d].Status: got %q, want %q", i, got[i].Status, want.Status)
					}
				}
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
}
