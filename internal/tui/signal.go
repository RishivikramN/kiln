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

// newChatContext creates a context with a 5-minute timeout for a single chat turn.
func newChatContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}
