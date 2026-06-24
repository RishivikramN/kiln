package tui

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// watchResize listens for SIGWINCH and redraws on resize.
// The caller closes sigCh to stop.
func watchResize(sigCh chan os.Signal, t *TUI) {
	signal.Notify(sigCh, syscall.SIGWINCH)
	for range sigCh {
		t.getTermSize()
		t.render()
	}
	signal.Stop(sigCh)
}

// newChatContext creates a context with the configured timeout for one chat turn.
func (t *TUI) newChatContext() (context.Context, context.CancelFunc) {
	timeout := t.chatTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return context.WithTimeout(context.Background(), timeout)
}
