package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	ansiReset       = "\033[0m"
	ansiBold        = "\033[1m"
	ansiDim         = "\033[2m"
	ansiGreen       = "\033[32m"
	ansiCyan        = "\033[36m"
	ansiBlue        = "\033[34m"
	ansiClearScreen = "\033[2J\033[H"
	ansiHideCursor  = "\033[?25l"
	ansiShowCursor  = "\033[?25h"
	ansiClearLine   = "\033[2K"
	ansiBgDark      = "\033[48;5;236m"
)

func moveTo(row, col int) string {
	return fmt.Sprintf("\033[%d;%dH", row, col)
}

type Message struct {
	Role    string
	Content string
	Tokens  int // estimated tokens for tool messages (input+output / 4)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type TUI struct {
	mu sync.Mutex // serialises render + state writes

	messages     []Message
	input        []rune
	model        string
	repo         string
	repoPath     string
	systemPrompt string

	// input history (shell-style ↑/↓)
	history    []string
	historyIdx int
	historyTmp string

	// spinner state (atomic so goroutine can update without the mutex)
	spinning     int32 // 1 while waiting for first token
	spinnerIdx   int32
	spinnerStart time.Time // set under mu when spinning starts

	width        int
	height       int
	scrollOffset int
	quit         bool
	responding   bool

	provider  Provider
	providers map[string]Provider
	permStore *PermStore

	origTermios syscall.Termios
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func NewTUI() *TUI {
	cwd, _ := os.Getwd()
	t := &TUI{
		model:      "none",
		repo:       filepath.Base(cwd),
		repoPath:   cwd,
		providers:  make(map[string]Provider),
		historyIdx: -1,
	}

	for _, try := range []func() (Provider, error){
		func() (Provider, error) { return NewClaudeProvider() },
		func() (Provider, error) { return NewOpenAIProvider() },
		func() (Provider, error) { return NewGeminiProvider() },
		func() (Provider, error) { return NewOllamaProvider() },
	} {
		if p, err := try(); err == nil {
			t.providers[p.Name()] = p
		}
	}

	for _, name := range []string{"claude", "openai", "gemini", "ollama"} {
		if p, ok := t.providers[name]; ok {
			t.provider = p
			t.model = p.Name() + "/" + p.ActiveModel()
			break
		}
	}

	if ps, err := LoadPermStore(); err == nil {
		t.permStore = ps
	}
	return t
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

func (t *TUI) Run() error {
	t.getTermSize()

	if err := t.enableRawMode(); err != nil {
		return fmt.Errorf("failed to enable raw mode: %w", err)
	}
	defer t.disableRawMode()
	defer fmt.Print(ansiShowCursor)
	defer fmt.Print(ansiClearScreen)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			t.getTermSize()
			t.render()
		}
	}()
	defer signal.Stop(sigCh)

	if t.provider == nil {
		t.addSystem("no provider connected\nset ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or start Ollama")
	} else {
		connected := make([]string, 0, len(t.providers))
		for _, name := range []string{"claude", "openai", "gemini", "ollama"} {
			if _, ok := t.providers[name]; ok {
				connected = append(connected, name)
			}
		}
		t.addSystem(fmt.Sprintf("welcome to kiln — providers: %s\ntype /help for commands", strings.Join(connected, ", ")))
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

		if t.responding {
			t.runChat()
		}
	}
}

func (t *TUI) handleKey(b []byte) bool {
	if len(b) == 0 {
		return true
	}

	// single-byte control codes
	if len(b) == 1 {
		switch b[0] {
		case 3, 4: // Ctrl+C, Ctrl+D
			return false
		case 12: // Ctrl+L — redraw
			return true
		case 127: // Backspace
			if len(t.input) > 0 {
				t.input = t.input[:len(t.input)-1]
			}
			t.historyIdx = -1
			return true
		case 13: // Enter
			t.submit()
			return !t.quit
		}
	}

	// escape sequences
	if len(b) >= 3 && b[0] == 27 && b[1] == '[' {
		switch {
		case b[2] == 'A': // ↑ — history prev
			t.historyPrev()
		case b[2] == 'B': // ↓ — history next
			t.historyNext()
		case b[2] == '5' && len(b) >= 4 && b[3] == '~': // PgUp — scroll up
			t.scrollOffset += t.height / 2
		case b[2] == '6' && len(b) >= 4 && b[3] == '~': // PgDn — scroll down
			t.scrollOffset -= t.height / 2
			if t.scrollOffset < 0 {
				t.scrollOffset = 0
			}
		}
		return !t.quit
	}

	// printable chars (ASCII + UTF-8)
	if b[0] >= 32 {
		for _, r := range string(b) {
			t.input = append(t.input, r)
		}
		t.historyIdx = -1
	}
	return !t.quit
}

func (t *TUI) historyPrev() {
	if len(t.history) == 0 {
		return
	}
	if t.historyIdx == -1 {
		t.historyTmp = string(t.input)
		t.historyIdx = len(t.history) - 1
	} else if t.historyIdx > 0 {
		t.historyIdx--
	}
	t.input = []rune(t.history[t.historyIdx])
}

func (t *TUI) historyNext() {
	if t.historyIdx == -1 {
		return
	}
	if t.historyIdx < len(t.history)-1 {
		t.historyIdx++
		t.input = []rune(t.history[t.historyIdx])
	} else {
		t.historyIdx = -1
		t.input = []rune(t.historyTmp)
	}
}

func (t *TUI) submit() {
	text := strings.TrimSpace(string(t.input))
	t.input = t.input[:0]
	t.historyIdx = -1
	if text == "" {
		return
	}

	// record in history (deduplicate consecutive identical entries)
	if len(t.history) == 0 || t.history[len(t.history)-1] != text {
		t.history = append(t.history, text)
	}

	if strings.HasPrefix(text, "/") {
		t.handleCommand(text)
		return
	}
	if t.provider == nil {
		t.addSystem("no provider connected — set an API key and restart")
		return
	}
	t.messages = append(t.messages, Message{Role: "user", Content: text})
	t.responding = true
	t.scrollOffset = 0
}

func (t *TUI) runChat() {
	t.responding = false

	var history []Message
	for _, m := range t.messages {
		if m.Role == "user" || m.Role == "assistant" {
			history = append(history, m)
		}
	}

	// add empty assistant placeholder — spinner will animate it
	t.messages = append(t.messages, Message{Role: "assistant", Content: ""})
	idx := len(t.messages) - 1

	// start spinner
	t.mu.Lock()
	t.spinnerStart = time.Now()
	t.mu.Unlock()
	atomic.StoreInt32(&t.spinning, 1)
	atomic.StoreInt32(&t.spinnerIdx, 0)
	spinnerDone := make(chan struct{})
	go func() {
		defer close(spinnerDone)
		for atomic.LoadInt32(&t.spinning) == 1 {
			time.Sleep(100 * time.Millisecond)
			atomic.AddInt32(&t.spinnerIdx, 1)
			t.render()
		}
	}()

	t.render()

	// select tools based on permission level
	var tools []Tool
	if t.permStore != nil {
		rp, ps := t.repoPath, t.permStore
		var base []Tool
		switch {
		case ps.CanWrite(rp):
			base = defaultTools() // all 4
		case ps.CanRead(rp):
			base = readTools() // list_files + read_file only
		}
		for i := range base {
			orig := base[i].Execute
			base[i].Execute = func(_ string, _ *PermStore, input map[string]any) (string, error) {
				return orig(rp, ps, input)
			}
		}
		tools = base
	}

	sysPrompt := systemPrompt

	// build explicit tool list so the model never has to guess what it has
	var toolNames []string
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
	}
	toolList := "none"
	if len(toolNames) > 0 {
		toolList = strings.Join(toolNames, ", ")
	}

	permState := "none — you have no tools. Tell the user to run /permissions rw or /permissions ro before doing anything."
	if t.permStore != nil {
		if p, ok := t.permStore.Get(t.repoPath); ok {
			switch p.Mode {
			case "read-write":
				permState = "read-write"
			case "read-only":
				permState = "read-only"
			}
		}
	}

	sysPrompt += fmt.Sprintf(
		"\n\nCURRENT SESSION:\nWorking directory: %s\nPermission level: %s\nTools you have RIGHT NOW (call these yourself, do not mention them to the user): %s",
		t.repoPath, permState, toolList,
	)

	if t.systemPrompt != "" {
		sysPrompt += "\n\nSession instructions: " + t.systemPrompt
	}

	firstToken := true
	err := t.provider.Chat(context.Background(), sysPrompt, history, tools,
		func(token string) {
			if firstToken {
				// stop spinner before first write
				atomic.StoreInt32(&t.spinning, 0)
				<-spinnerDone
				firstToken = false
			}
			t.mu.Lock()
			t.messages[idx].Content += token
			t.mu.Unlock()
			t.render()
		},
		func(name, input, result string) {
			approxTokens := (len(input) + len(result)) / 4
			t.mu.Lock()
			t.messages = append(t.messages, Message{
				Role:    "tool",
				Content: name,
				Tokens:  approxTokens,
			})
			// keep assistant placeholder at end
			last := t.messages[idx]
			t.messages = append(t.messages[:idx], t.messages[idx+1:]...)
			t.messages = append(t.messages, last)
			idx = len(t.messages) - 1
			t.mu.Unlock()
			t.render()
		},
	)

	// ensure spinner is stopped on error / no tokens
	atomic.StoreInt32(&t.spinning, 0)
	<-spinnerDone

	if err != nil {
		t.mu.Lock()
		t.messages[idx].Content = "error: " + err.Error()
		t.mu.Unlock()
		t.render()
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (t *TUI) handleCommand(cmd string) {
	parts := strings.Fields(cmd)
	switch parts[0] {
	case "/help":
		t.addSystem(strings.Join([]string{
			"slash commands:",
			"  /models                — list all available models",
			"  /models <name>         — switch model (e.g. openai/gpt-4o)",
			"  /permissions           — show repo permissions",
			"  /permissions allow     — grant read+write access",
			"  /permissions read-only — grant read-only access",
			"  /permissions deny      — revoke access",
			"  /system <prompt>       — set system prompt",
			"  /system                — clear system prompt",
			"  /undo                  — remove last exchange",
			"  /copy                  — copy last response to clipboard",
			"  /clear                 — clear chat history",
			"  /exit                  — quit",
			"",
			"keyboard:",
			"  ↑ / ↓      — browse input history",
			"  PgUp / PgDn — scroll chat",
			"  Ctrl+L     — redraw",
			"  Ctrl+C     — exit",
		}, "\n"))
	case "/clear":
		t.messages = nil
		t.scrollOffset = 0
	case "/exit":
		t.quit = true
	case "/models":
		t.handleModels(parts[1:])
	case "/permissions":
		t.handlePermissions(parts[1:])
	case "/system":
		t.handleSystem(parts[1:])
	case "/undo":
		t.handleUndo()
	case "/copy":
		t.handleCopy()
	default:
		t.addSystem("unknown command: " + parts[0] + " — type /help for commands")
	}
	if !t.quit {
		t.scrollOffset = 0
	}
}

func (t *TUI) handleModels(args []string) {
	if len(t.providers) == 0 {
		t.addSystem("no providers connected\nset ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, or start Ollama")
		return
	}
	if len(args) == 0 {
		var lines []string
		for _, pname := range []string{"claude", "openai", "gemini", "ollama"} {
			p, ok := t.providers[pname]
			if !ok {
				continue
			}
			lines = append(lines, fmt.Sprintf("  [%s]", pname))
			for _, m := range p.Models() {
				full := pname + "/" + m
				if t.provider == p && m == p.ActiveModel() {
					lines = append(lines, "    ● "+full)
				} else {
					lines = append(lines, "    ○ "+full)
				}
			}
		}
		t.addSystem("available models — /models <provider/model> to switch:\n" + strings.Join(lines, "\n"))
		return
	}

	target := args[0]
	providerName, modelName, hasSep := strings.Cut(target, "/")
	if !hasSep {
		modelName = providerName
		providerName = ""
	}

	for _, pname := range []string{"claude", "openai", "gemini", "ollama"} {
		if providerName != "" && pname != providerName {
			continue
		}
		p, ok := t.providers[pname]
		if !ok {
			continue
		}
		if err := p.SetModel(modelName); err == nil {
			t.provider = p
			t.model = pname + "/" + p.ActiveModel()
			t.addSystem("switched to " + t.model)
			return
		}
	}
	t.addSystem(fmt.Sprintf("model %q not found — use /models to list available models", target))
}

func (t *TUI) handlePermissions(args []string) {
	if t.permStore == nil {
		t.addSystem("permission store unavailable")
		return
	}
	if len(args) == 0 {
		all := t.permStore.All()
		if len(all) == 0 {
			t.addSystem("no repos registered yet\nuse /permissions allow, /permissions read-only, or /permissions deny")
			return
		}
		var lines []string
		for path, perm := range all {
			marker := "  "
			if path == t.repoPath {
				marker = "● "
			}
			lines = append(lines, fmt.Sprintf("  %s[%s] %s", marker, modeShort(perm.Mode), path))
		}
		t.addSystem("repo permissions (● = current):\n" + strings.Join(lines, "\n") +
			"\n\nuse /permissions allow · /permissions read-only · /permissions deny")
		return
	}

	var mode string
	switch args[0] {
	case "allow", "read-write":
		mode = "read-write"
	case "read-only":
		mode = "read-only"
	case "deny", "none":
		mode = "none"
	default:
		t.addSystem("usage: /permissions allow · /permissions read-only · /permissions deny")
		return
	}
	if err := t.permStore.Set(t.repoPath, mode); err != nil {
		t.addSystem("error: " + err.Error())
		return
	}
	t.addSystem(fmt.Sprintf("set %s → %s", t.repo, mode))
}

func (t *TUI) handleSystem(args []string) {
	if len(args) == 0 {
		if t.systemPrompt == "" {
			t.addSystem("no system prompt set\nuse /system <text> to set one")
		} else {
			t.systemPrompt = ""
			t.addSystem("system prompt cleared")
		}
		return
	}
	t.systemPrompt = strings.Join(args, " ")
	t.addSystem(fmt.Sprintf("system prompt set:\n  %s", t.systemPrompt))
}

func (t *TUI) handleUndo() {
	// remove trailing assistant message if present
	if len(t.messages) > 0 && t.messages[len(t.messages)-1].Role == "assistant" {
		t.messages = t.messages[:len(t.messages)-1]
	}
	// remove the user message before it
	if len(t.messages) > 0 && t.messages[len(t.messages)-1].Role == "user" {
		t.messages = t.messages[:len(t.messages)-1]
		t.addSystem("last exchange removed")
	} else {
		t.addSystem("nothing to undo")
	}
}

func (t *TUI) handleCopy() {
	// find last assistant message
	for i := len(t.messages) - 1; i >= 0; i-- {
		if t.messages[i].Role == "assistant" {
			content := t.messages[i].Content
			cmd := exec.Command("pbcopy")
			cmd.Stdin = strings.NewReader(content)
			if err := cmd.Run(); err != nil {
				t.addSystem("copy failed: " + err.Error())
			} else {
				t.addSystem("copied to clipboard")
			}
			return
		}
	}
	t.addSystem("nothing to copy")
}

func (t *TUI) addSystem(msg string) {
	t.messages = append(t.messages, Message{Role: "system", Content: msg})
}

func (t *TUI) render() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.renderLocked()
}

func (t *TUI) renderLocked() {
	var sb strings.Builder
	sb.WriteString(ansiHideCursor)
	sb.WriteString(ansiClearScreen)

	// Status bar
	permLabel := "no permissions"
	if t.permStore != nil {
		if p, ok := t.permStore.Get(t.repoPath); ok {
			switch p.Mode {
			case "read-write":
				permLabel = "read and write"
			case "read-only":
				permLabel = "read only"
			case "none":
				permLabel = "no permissions"
			}
		}
	}
	sysIndicator := ""
	if t.systemPrompt != "" {
		sysIndicator = "  [sys]"
	}
	left := fmt.Sprintf(" kiln │  %s  │  permissions: %s%s  ", t.model, permLabel, sysIndicator)
	right := "  Ctrl+C to exit "
	pad := t.width - len(left) - len(right)
	if pad < 0 {
		pad = 0
	}
	sb.WriteString(moveTo(1, 1))
	sb.WriteString(ansiCyan + ansiBold + left + strings.Repeat(" ", pad) + right + ansiReset)

	// Top divider
	sb.WriteString(moveTo(2, 1))
	sb.WriteString(ansiDim + strings.Repeat("─", t.width) + ansiReset)

	// Chat area
	chatHeight := t.height - 4
	if chatHeight < 1 {
		chatHeight = 1
	}
	lines := t.chatLines()

	maxScroll := len(lines) - chatHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if t.scrollOffset > maxScroll {
		t.scrollOffset = maxScroll
	}

	start := len(lines) - chatHeight - t.scrollOffset
	if start < 0 {
		start = 0
	}

	for i := 0; i < chatHeight; i++ {
		sb.WriteString(moveTo(3+i, 1))
		sb.WriteString(ansiClearLine)
		if idx := start + i; idx < len(lines) {
			sb.WriteString(lines[idx])
		}
	}

	// Bottom divider
	sb.WriteString(moveTo(t.height-1, 1))
	sb.WriteString(ansiDim + strings.Repeat("─", t.width) + ansiReset)

	// Input line
	sb.WriteString(moveTo(t.height, 1))
	sb.WriteString(ansiClearLine)
	sb.WriteString(ansiGreen + ansiBold + "  > " + ansiReset + string(t.input) + "█")

	fmt.Print(sb.String())
}

func (t *TUI) chatLines() []string {
	var lines []string
	w := t.width
	textWidth := w - 4
	if textWidth < 10 {
		textWidth = 10
	}

	for i, msg := range t.messages {
		switch msg.Role {

		case "user":
			// full-width dark bar, like Claude Code prompt
			first := true
			for _, rawLine := range strings.Split(msg.Content, "\n") {
				pfx := "    "
				if first {
					pfx = "  ❯ "
					first = false
				}
				for _, line := range wrap(rawLine, w-len(pfx)) {
					raw := pfx + line
					padded := fmt.Sprintf("%-*s", w, raw)
					lines = append(lines, ansiBgDark+ansiDim+padded+ansiReset)
				}
			}
			lines = append(lines, "")

		case "tool":
			tok := ""
			if msg.Tokens > 0 {
				tok = fmt.Sprintf(" · ~%d tokens", msg.Tokens)
			}
			lines = append(lines, ansiDim+"  ⚙ "+toolSummary(msg.Content)+tok+ansiReset)

		case "assistant":
			content := msg.Content
			if content == "" && i == len(t.messages)-1 && atomic.LoadInt32(&t.spinning) == 1 {
				frame := spinnerFrames[int(atomic.LoadInt32(&t.spinnerIdx))%len(spinnerFrames)]
				elapsed := formatElapsed(time.Since(t.spinnerStart))
				lines = append(lines, ansiDim+"  "+frame+" thinking… "+elapsed+ansiReset)
				lines = append(lines, "")
				continue
			}
			first := true
			for _, rawLine := range strings.Split(content, "\n") {
				for _, line := range wrap(rawLine, textWidth) {
					if first {
						lines = append(lines, "  "+ansiGreen+ansiBold+"●"+ansiReset+" "+line)
						first = false
					} else {
						lines = append(lines, "    "+line)
					}
				}
			}
			lines = append(lines, "")

		case "system":
			// slash command output (not tool calls)
			first := true
			for _, rawLine := range strings.Split(msg.Content, "\n") {
				for _, line := range wrap(rawLine, textWidth) {
					if first {
						lines = append(lines, "  "+ansiCyan+ansiBold+"●"+ansiReset+" "+line)
						first = false
					} else {
						lines = append(lines, "    "+line)
					}
				}
			}
			lines = append(lines, "")
		}
	}
	return lines
}

func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

func toolSummary(name string) string {
	switch name {
	case "list_files":
		return "Listed files"
	case "read_file":
		return "Read file"
	case "write_file":
		return "Wrote file"
	case "run_command":
		return "Ran shell command"
	default:
		return name
	}
}

func wrap(text string, width int) []string {
	if len(text) <= width {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
