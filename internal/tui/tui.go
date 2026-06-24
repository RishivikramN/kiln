package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"kiln/internal/permissions"
	"kiln/internal/provider"
	"kiln/internal/session"
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

	// slash-command autocomplete
	completions   []string
	completionIdx int // -1 when popup is hidden

	// spinner state (atomic so goroutine can update without the mutex)
	spinning     int32 // 1 while waiting for first token
	spinnerIdx   int32
	spinnerStart time.Time // set under mu when spinning starts

	width        int
	height       int
	scrollOffset  int
	quit          bool
	responding    bool
	chatCancel    func() // non-nil while a Chat() call is in flight
	lastTitle     string // last title sent to the terminal; avoids redundant escapes
	contextTokens int    // actual input-token count from last provider call (0 = use estimate)

	// runtime configuration (set via Configure before Run)
	chatTimeout     time.Duration
	autoSaveSession bool
	confirmWrites   bool

	// write-confirmation handshake (set by runChat goroutine, cleared after reply)
	pendingConfirm *confirmReq

	activeProvider provider.Provider
	providers      map[string]provider.Provider
	permStore      *permissions.PermStore

	origTermios syscall.Termios
}

// confirmReq is a pending confirmation request from the tool-execution path.
type confirmReq struct {
	label   string    // short description shown to the user
	replyCh chan bool  // buffered(1); caller sends true=proceed false=skip
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
		model:           "none",
		repo:            filepath.Base(cwd),
		repoPath:        cwd,
		providers:       make(map[string]provider.Provider),
		historyIdx:      -1,
		completionIdx:   -1,
		chatTimeout:     5 * time.Minute,
		autoSaveSession: true,
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

// Options holds user-configurable TUI settings, typically loaded from config.
type Options struct {
	MaxToolCalls    int
	ChatTimeout     time.Duration
	ConfirmWrites   bool
	AutoSaveSession bool // default true in NewTUI; explicit false disables
	SystemPrompt    string
	DefaultModel    string
}

// Configure applies user config to the TUI and all registered providers.
// Must be called after AddProvider so providers can receive SetMaxToolCalls.
func (t *TUI) Configure(opts Options) {
	if opts.MaxToolCalls > 0 {
		for _, p := range t.providers {
			p.SetMaxToolCalls(opts.MaxToolCalls)
		}
	}
	if opts.ChatTimeout > 0 {
		t.chatTimeout = opts.ChatTimeout
	}
	t.confirmWrites = opts.ConfirmWrites
	// AutoSaveSession is true by default; only override when explicitly set to false.
	if !opts.AutoSaveSession {
		t.autoSaveSession = false
	}
	if opts.SystemPrompt != "" {
		t.systemPrompt = opts.SystemPrompt
	}
	if opts.DefaultModel != "" {
		parts := strings.SplitN(opts.DefaultModel, "/", 2)
		if len(parts) == 2 {
			pname, mname := parts[0], parts[1]
			if p, ok := t.providers[pname]; ok {
				if p.SetModel(mname) == nil {
					t.activeProvider = p
					t.model = opts.DefaultModel
				}
			}
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
	defer fmt.Print("\033]0;\007") // restore blank tab title on exit

	sigCh := make(chan os.Signal, 1)
	// Use os/signal indirectly — import via syscall.SIGWINCH
	go watchResize(sigCh, t)
	defer close(sigCh)

	// Restore saved session before showing welcome message.
	sessionRestored := false
	if t.autoSaveSession {
		if sess, err := session.Load(t.repoPath); err == nil && len(sess.Messages) > 0 {
			t.messages = sess.ToProviderMessages()
			if sess.SystemPrompt != "" && t.systemPrompt == "" {
				t.systemPrompt = sess.SystemPrompt
			}
			if sess.Model != "" {
				parts := strings.SplitN(sess.Model, "/", 2)
				if len(parts) == 2 {
					if p, ok := t.providers[parts[0]]; ok {
						if p.SetModel(parts[1]) == nil {
							t.activeProvider = p
							t.model = sess.Model
						}
					}
				}
			}
			t.addSystem("session restored — /sessions clear to start fresh")
			sessionRestored = true
		}
	}

	if t.activeProvider == nil {
		t.addSystem("no provider connected\nset ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or start Ollama")
	} else if !sessionRestored {
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
			ctx, cancel := t.newChatContext()
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

// tokenCount returns the best available context-size estimate in tokens.
// When the provider has reported real usage, that takes priority over chars/4.
// Safe to call from renderLocked (t.mu already held) — must not re-acquire t.mu.
func (t *TUI) tokenCount() int {
	if t.contextTokens > 0 {
		return t.contextTokens
	}
	chars := len(systemPrompt) + 300 // base prompt + session-context injection
	for _, m := range t.messages {
		switch m.Role {
		case "user", "assistant",
			provider.RoleHistAst, provider.RoleHistUsr,
			provider.RoleHistAstOAI, provider.RoleHistUsrOAI,
			provider.RoleHistAstClaude, provider.RoleHistUsrClaude,
			provider.RoleHistAstGemini, provider.RoleHistUsrGemini:
			chars += len(m.Content) + 8
		}
	}
	return chars / 4
}

// tabTitle returns the terminal tab title for the current conversation.
// Called from renderLocked — must not acquire t.mu.
func (t *TUI) tabTitle() string {
	for _, m := range t.messages {
		if m.Role == "user" {
			text := strings.TrimSpace(m.Content)
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[:i]
			}
			text = strings.TrimSpace(text)
			if len(text) == 0 {
				continue
			}
			if len(text) > 40 {
				text = text[:38] + "…"
			}
			return "kiln — " + text
		}
	}
	return "kiln — " + t.repo
}

func (t *TUI) contextWindow() int {
	if t.activeProvider == nil {
		return 0
	}
	return t.activeProvider.ContextWindow()
}

// render acquires the lock and renders the TUI.
func (t *TUI) render() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.renderLocked()
}

