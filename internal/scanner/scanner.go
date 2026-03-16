// Package scanner aggregates session discovery across all supported providers.
package scanner

import (
	"context"
	"os"
	"sort"
	"sync"

	"github.com/universe/claude-monitor/internal/model"
)

// Scanner aggregates results from all provider-specific scanners.
type Scanner struct {
	claude *claudeScanner
	codex  *codexScanner
}

// New creates a new Scanner.
func New() *Scanner {
	home, _ := os.UserHomeDir()
	return &Scanner{
		claude: newClaudeScanner(home),
		codex:  newCodexScanner(home),
	}
}

// Scan discovers sessions from all supported providers and returns them
// sorted by LastActive descending.
func (s *Scanner) Scan(ctx context.Context) ([]model.SessionInfo, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sessions []model.SessionInfo
		scanErr  error
	)

	// Claude Code scanner.
	wg.Add(1)
	go func() {
		defer wg.Done()
		results, err := s.claude.scan(ctx)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			scanErr = err
			return
		}
		sessions = append(sessions, results...)
	}()

	// Codex CLI scanner.
	if s.codex != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := s.codex.scan(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				scanErr = err
				return
			}
			sessions = append(sessions, results...)
		}()
	}

	wg.Wait()

	if scanErr != nil {
		return nil, scanErr
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	return sessions, nil
}
