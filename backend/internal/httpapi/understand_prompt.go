package httpapi

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// understandPromptTTL bounds how stale the offered channel subcategories may
// be: a channel labelled a minute ago is filterable from Ask a minute later.
// The prompt itself only changes when the set does, so the provider's prompt
// cache survives the rebuilds.
const understandPromptTTL = time.Minute

type understandPromptCache struct {
	mu      sync.Mutex
	prompt  string
	expires time.Time
}

// understandPrompt is the understand step's system prompt, offering the
// channel subcategories some channel carries today. Every failure degrades to
// the prompt without them: Ask still answers, it just cannot filter by kind
// of channel. A failure is not cached, so the next question tries again.
func (s *server) understandPrompt(ctx context.Context) string {
	if s.channels == nil {
		return understandSystemPrompt
	}
	c := &s.understandPromptCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.prompt
	}
	byChannel, err := s.channels.AllSubcategories(ctx)
	if err != nil {
		slog.WarnContext(ctx, "understand: channel subcategories unavailable", "err", err)
		return understandSystemPrompt
	}
	var inUse []string
	for _, keys := range byChannel {
		for _, k := range keys {
			if !slices.Contains(inUse, k) {
				inUse = append(inUse, k)
			}
		}
	}
	slices.Sort(inUse)
	c.prompt, c.expires = buildUnderstandPrompt(inUse), time.Now().Add(understandPromptTTL)
	return c.prompt
}
