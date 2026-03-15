// Package scanner aggregates session discovery across all supported providers.
package scanner

import (
	"context"

	"github.com/universe/claude-monitor/internal/model"
)

// Scanner aggregates results from all provider-specific scanners.
type Scanner struct{}

// New creates a new Scanner.
func New() *Scanner {
	return &Scanner{}
}

// Scan discovers sessions from all supported providers and returns them
// sorted by LastActive descending.
func (s *Scanner) Scan(ctx context.Context) ([]model.SessionInfo, error) {
	return nil, nil
}
