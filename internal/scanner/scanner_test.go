package scanner

import (
	"context"
	"testing"
)

func TestNew(t *testing.T) {
	sc := New()
	if sc == nil {
		t.Fatal("New() returned nil")
	}
}

func TestScanner_Scan(t *testing.T) {
	tests := map[string]struct {
		ctx       context.Context
		wantCount int
		wantErr   bool
	}{
		"returns_no_error_with_valid_context": {
			ctx:       context.Background(),
			wantCount: 0, // stub returns nil slice
			wantErr:   false,
		},
		"returns_no_error_with_cancelled_context": {
			// Once implemented, a cancelled context should propagate;
			// for now the stub ignores it. This test documents the expected contract.
			ctx:       cancelledContext(),
			wantCount: 0,
			wantErr:   false, // stub; will change when implemented
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sc := New()
			sessions, err := sc.Scan(tt.ctx)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if len(sessions) != tt.wantCount {
				t.Errorf("session count: got %d, want %d", len(sessions), tt.wantCount)
			}
		})
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
