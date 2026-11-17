// Package bot routes one inbound message to a reply. It's shared by the
// Telegram client (real messages) and `denly simulate` (local testing).
package bot

import (
	"strings"
	"sync"

	"denly/internal/commands"
	"denly/internal/format"
)

// FreeText understands plain-English messages (the Claude parser, phase 4).
type FreeText interface {
	Handle(text string) (string, error)
}

type Bot struct {
	Cmd    *commands.Handler
	F      *format.Formatter
	Parser FreeText // nil = free text unavailable (e.g. no Anthropic key)

	mu sync.Mutex // one message at a time: order matters for display numbers
}

// HandleInbound returns the reply to send for text. Routing order:
// quick command → parser (which handles any pending clarification) → fallback.
func (b *Bot) HandleInbound(text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	text = strings.TrimSpace(text)
	if text == "" {
		return b.F.Help(), nil
	}
	if reply, handled, err := b.Cmd.Handle(text); handled || err != nil {
		return reply, err
	}
	if b.Parser == nil {
		return b.F.NoParser(), nil
	}
	return b.Parser.Handle(text)
}
