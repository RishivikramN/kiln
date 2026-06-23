package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"kiln/internal/permissions"
	"kiln/internal/provider"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// TUI is the main terminal user interface state.
type TUI struct {
	mu sync.Mutex // serialises render + state writes

	messages     []provider.Message
	input        []rune
	model        string
	repo         string
	repoPath     string
	systemPrompt string

	// input history (shell-style ↑/↓)
	history    []string
	historyIdx int
	historyTmp string
	cursorPos  int // insertion point within t.input (rune index)

	// spinner state (atomic so goroutine can update without the mutex)
	spinning     int32 // 1 while waiting for first token
	spinnerIdx   int32
	spinnerStart time.Time // set under mu when spinning starts

	width        int
	height       int
	scrollOffset int
	quit         bool
	responding   bool
	chatCancel   func() // non-nil while a Chat() call is in flight

	activeProvider  provider.Provider
	providers       map[string]provider.Provider
	permStore       *permissions.PermStore

	origTermios syscall.Termios
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// NewTUI creates a minimal TUI without any providers registered.
// Call AddProvider to register providers, then Run to start.
func NewTUI() *TUI {
	cwd, _ := os.Getwd()
	t := &TUI{
		model:      "none",
		repo:       filepath.Base(cwd),
		repoPath:   cwd,
		providers:  make(map[string]provider.Provider),
		historyIdx: -1,
	}
	if ps, err := permissions.LoadPermStore(); err == nil {
		t.permStore = ps
	}
	return t
}

// AddProvider registers a provider. The first registered provider (in priority
// order: claude > openai > gemini > ollama) becomes the active provider.
func (t *TUI) AddProvider(p provider.Provider) {
	t.providers[p.Name()] = p
	// Set as active provider if we don't have one yet, or if this one has higher priority.
	for _, name := range []string{"claude", "openai", "gemini", "ollama"} {
		if p.Name() == name {
			// Check if there's already a higher-priority provider active.
			if t.activeProvider == nil {
				t.activeProvider = p
				t.model = p.Name() + "/" + p.ActiveModel()
			} else {
				// If the current active is lower priority, replace it.
				currentPriority := providerPriority(t.activeProvider.Name())
				newPriority := providerPriority(name)
				if newPriority < currentPriority {
					t.activeProvider = p
					t.model = p.Name() + "/" + p.ActiveModel()
				}
			}
			break
		}
	}
}

func providerPriority(name string) int {
	for i, n := range []string{"claude", "openai", "gemini", "ollama"} {
		if n == name {
			return i
		}
	}
	return 999
}

func (t *TUI) getTermSize() {
	var ws winsize
	syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdout),
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&ws)))
	t.width = int(ws.Col)
	t.height = int(ws.Row)
	if t.width < 10 {
		t.width = 80
	}
	if t.height < 5 {
		t.height = 24
	}
}

func (t *TUI) enableRawMode() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdin),
		syscall.TIOCGETA,
		uintptr(unsafe.Pointer(&t.origTermios))); errno != 0 {
		return errno
	}
	raw := t.origTermios
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.BRKINT | syscall.INPCK | syscall.ISTRIP
	raw.Cflag |= syscall.CS8
	raw.Oflag &^= syscall.OPOST
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdin),
		syscall.TIOCSETA,
		uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return errno
	}
	return nil
}

func (t *TUI) disableRawMode() {
	syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdin),
		syscall.TIOCSETA,
		uintptr(unsafe.Pointer(&t.origTermios)))
}

// Run starts the terminal event loop.
func (t *TUI) Run() error {
	t.getTermSize()

	if err := t.enableRawMode(); err != nil {
		return fmt.Errorf("failed to enable raw mode: %w", err)
	}
	defer t.disableRawMode()
	defer fmt.Print(ansiShowCursor)
	defer fmt.Print(ansiClearScreen)

	sigCh := make(chan os.Signal, 1)
	// Use os/signal indirectly — import via syscall.SIGWINCH
	go watchResize(sigCh, t)
	defer close(sigCh)

	if t.activeProvider == nil {
		t.addSystem("no provider connected\nset ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or start Ollama")
	} else {
		connected := make([]string, 0, len(t.providers))
		for _, name := range []string{"claude", "openai", "gemini", "ollama"} {
			if _, ok := t.providers[name]; ok {
				connected = append(connected, name)
			}
		}
		connectedStr := ""
		for i, n := range connected {
			if i > 0 {
				connectedStr += ", "
			}
			connectedStr += n
		}
		t.addSystem(fmt.Sprintf("welcome to kiln — providers: %s\ntype /help for commands", connectedStr))
	}
	if t.permStore != nil {
		if _, known := t.permStore.Get(t.repoPath); !known {
			t.addSystem(fmt.Sprintf("new repo: %s\nno permissions set — use /permissions allow or /permissions read-only", t.repoPath))
		}
	}
	t.render()

	buf := make([]byte, 32)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil
		}
		if !t.handleKey(buf[:n]) {
			return nil
		}
		t.render()

		t.mu.Lock()
		shouldStart := t.responding && t.chatCancel == nil
		t.mu.Unlock()
		if shouldStart {
			ctx, cancel := newChatContext()
			t.mu.Lock()
			t.chatCancel = cancel
			t.mu.Unlock()
			go func() {
				t.runChat(ctx)
				cancel()
				t.mu.Lock()
				t.chatCancel = nil
				t.mu.Unlock()
				t.render()
			}()
		}
	}
}

func (t *TUI) addSystem(msg string) {
	t.messages = append(t.messages, provider.Message{Role: "system", Content: msg})
}

// render acquires the lock and renders the TUI.
func (t *TUI) render() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.renderLocked()
}

