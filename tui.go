package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

type viewState int

const (
	stateInput viewState = iota
	stateLoading
	stateResult
	stateConfirmRun
	stateError
)

type exitActionType int

const (
	actionNone exitActionType = iota
	actionInsert
	actionRun
	actionCopy
)

const exitCodeRun = 42

const (
	defaultModel  = "gpt-5.6-luna"
	modelCacheTTL = 24 * time.Hour
)

// CommandCandidate represents a single command alternative extracted from the AI response.
type CommandCandidate struct {
	Command string
}

// ParsedResponse holds the explanation and command candidates from an AI response.
type ParsedResponse struct {
	Explanation string
	Candidates  []CommandCandidate
}

// Messages
type modelsLoadedMsg struct {
	models []string
	err    error
}

type streamDeltaMsg struct {
	delta   string
	updates <-chan streamUpdate
}

type streamDoneMsg struct{ final string }
type streamErrMsg struct{ err error }

type model struct {
	state          viewState
	textInput      textinput.Model
	refineInput    textinput.Model
	spinner        spinner.Model
	copilot        *copilotClient
	models         []string
	modelIndex     int
	modelsReady    bool
	refreshModels  bool
	preferredModel string
	candidates     []CommandCandidate
	selectedIdx    int
	explanation    string
	streaming      string
	err            error
	modelWarning   string
	exitAction     exitActionType
	prompt         string
	shell          string
	bare           bool
	width          int
	height         int
}

func newModel(inlinePrompt string, bare bool) model {
	ti := textinput.New()
	ti.Placeholder = "Describe the command you need..."
	ti.Focus()
	ti.CharLimit = 500
	ti.Width = 60

	ri := textinput.New()
	ri.Placeholder = "Refine your request..."
	ri.CharLimit = 500
	ri.Width = 60

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	m := model{
		state:          stateInput,
		textInput:      ti,
		refineInput:    ri,
		spinner:        s,
		copilot:        newCopilotClient(),
		prompt:         inlinePrompt,
		shell:          detectShell(),
		bare:           bare,
		preferredModel: defaultModel,
	}
	if cfg, err := loadConfig(); err == nil {
		m.models = cfg.Models
		m.modelsReady = len(cfg.Models) > 0
		m.refreshModels = !modelCacheFresh(cfg.ModelsUpdatedAt, time.Now())
		if cfg.LastModel != "" {
			m.preferredModel = cfg.LastModel
		}
		for i, name := range m.models {
			if name == m.preferredModel {
				m.modelIndex = i
				break
			}
		}
	}
	if inlinePrompt != "" {
		m.state = stateLoading
	}
	return m
}

// selectedCommand returns the currently selected command text.
func (m model) selectedCommand() string {
	if len(m.candidates) == 0 {
		return ""
	}
	return m.candidates[m.selectedIdx].Command
}

func (m *model) saveSelectedModel() {
	if len(m.models) == 0 {
		return
	}
	m.preferredModel = m.models[m.modelIndex]
	if err := updateConfig(func(cfg *config) {
		cfg.LastModel = m.preferredModel
	}); err != nil {
		m.modelWarning = "Model selected, but the preference could not be saved"
	}
}

func modelCacheFresh(updatedAt int64, now time.Time) bool {
	if updatedAt <= 0 {
		return false
	}
	updated := time.Unix(updatedAt, 0)
	return !updated.After(now) && now.Sub(updated) < modelCacheTTL
}

func (m model) selectedModel() string {
	if len(m.models) > 0 {
		return m.models[m.modelIndex]
	}
	return m.preferredModel
}

// parseResponse extracts an explanation and runnable commands from the AI response.
// Supports the structured EXPLANATION:/COMMAND: format as well as fenced code blocks
// and bare command lines for backward compatibility.
func parseResponse(raw string) ParsedResponse {
	var resp ParsedResponse
	lines := strings.Split(raw, "\n")

	// First pass: check for structured EXPLANATION:/COMMAND: format
	hasStructured := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "COMMAND:") || strings.HasPrefix(trimmed, "EXPLANATION:") {
			hasStructured = true
			break
		}
	}

	if hasStructured {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "EXPLANATION:") {
				explanation := strings.TrimSpace(strings.TrimPrefix(trimmed, "EXPLANATION:"))
				if explanation != "" {
					if resp.Explanation != "" {
						resp.Explanation += " "
					}
					resp.Explanation += explanation
				}
			} else if strings.HasPrefix(trimmed, "COMMAND:") {
				cmd := strings.TrimSpace(strings.TrimPrefix(trimmed, "COMMAND:"))
				cmd = stripInlineBackticks(cmd)
				if cmd != "" {
					resp.Candidates = append(resp.Candidates, CommandCandidate{Command: cmd})
				}
			}
		}
		resp.Candidates = uniqueCandidates(resp.Candidates)
		return resp
	}

	// Fallback: fenced code blocks and bare command lines
	inFence := false
	fencePattern := regexp.MustCompile("^```")
	var fenceLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle code fence state
		if fencePattern.MatchString(trimmed) {
			if inFence && len(fenceLines) > 0 {
				cmd := strings.TrimSpace(strings.Join(fenceLines, "\n"))
				if cmd != "" {
					resp.Candidates = append(resp.Candidates, CommandCandidate{Command: cmd})
				}
				fenceLines = nil
			}
			inFence = !inFence
			continue
		}

		if inFence {
			fenceLines = append(fenceLines, trimmed)
			continue
		}

		// Outside fence: skip empty lines and markdown-like prose
		if trimmed == "" {
			continue
		}
		if isProseOrMarkdown(trimmed) {
			continue
		}

		// Strip leading $ prompt
		if strings.HasPrefix(trimmed, "$ ") {
			trimmed = strings.TrimPrefix(trimmed, "$ ")
		}
		trimmed = stripInlineBackticks(trimmed)

		resp.Candidates = append(resp.Candidates, CommandCandidate{Command: trimmed})
	}

	// Handle unclosed fence
	if inFence && len(fenceLines) > 0 {
		cmd := strings.TrimSpace(strings.Join(fenceLines, "\n"))
		if cmd != "" {
			resp.Candidates = append(resp.Candidates, CommandCandidate{Command: cmd})
		}
	}

	resp.Candidates = uniqueCandidates(resp.Candidates)
	return resp
}

func uniqueCandidates(candidates []CommandCandidate) []CommandCandidate {
	seen := make(map[string]struct{}, len(candidates))
	unique := make([]CommandCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		command := strings.TrimSpace(candidate.Command)
		if command == "" {
			continue
		}
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		unique = append(unique, CommandCandidate{Command: command})
	}
	return unique
}

func isProseOrMarkdown(s string) bool {
	lower := strings.ToLower(s)
	// Markdown headers, list markers with prose, "Or", "Note:", etc.
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, ">") {
		return true
	}
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		// list items that look like prose (contain spaces after first word)
		rest := strings.TrimPrefix(strings.TrimPrefix(s, "- "), "* ")
		if strings.Contains(rest, " ") && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "~") {
			return true
		}
	}
	proseStarts := []string{"or ", "note:", "alternatively", "you can", "this ", "on ", "if ", "for ", "the ", "use "}
	for _, p := range proseStarts {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Lines ending with : are usually labels
	if strings.HasSuffix(s, ":") {
		return true
	}
	return false
}

// stripInlineBackticks removes surrounding backticks from a command string.
func stripInlineBackticks(s string) string {
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		return s[1 : len(s)-1]
	}
	return s
}

type commandRisk struct {
	dangerous bool
	reason    string
}

// classifyCommand identifies commands that should never run without an explicit confirmation.
func classifyCommand(cmd string) commandRisk {
	lower := strings.ToLower(cmd)
	patterns := []struct {
		pattern string
		reason  string
	}{
		// POSIX
		{"rm -rf", "recursively deletes files"},
		{"rm -r ", "recursively deletes files"},
		{"rm -fr", "recursively deletes files"},
		{"sudo ", "runs with elevated privileges"},
		{"chmod -r", "recursively changes permissions"},
		{"chown -r", "recursively changes ownership"},
		{"kill -9", "forcefully terminates a process"},
		{"killall", "terminates processes by name"},
		{"dd ", "writes raw data to a device or file"},
		{"mkfs", "formats a filesystem"},
		{"> /dev/", "writes directly to a device"},
		{"docker system prune", "removes Docker resources"},
		{"kubectl delete", "deletes Kubernetes resources"},
		{"git reset --hard", "discards uncommitted changes"},
		{"git clean -f", "deletes untracked files"},
		{"git push --force", "rewrites remote history"},
		{"git push -f", "rewrites remote history"},
		{"drop database", "deletes a database"},
		{"drop table", "deletes a database table"},
		{"truncate table", "removes all rows from a table"},
		{"curl ", "downloads remote content"},
		{"wget ", "downloads remote content"},
		{":(){", "is a fork bomb"},
		{"fork bomb", "is a fork bomb"},
		// Windows / PowerShell
		{"remove-item", "deletes files or directories"},
		{"del /s", "recursively deletes files"},
		{"rd /s", "recursively deletes directories"},
		{"format-volume", "formats a volume"},
		{"clear-disk", "erases a disk"},
		{"remove-partition", "deletes a disk partition"},
		{"stop-computer", "shuts down the computer"},
		{"restart-computer", "restarts the computer"},
		{"stop-process -force", "forcefully terminates a process"},
	}
	for _, candidate := range patterns {
		if strings.Contains(lower, candidate.pattern) {
			if (candidate.pattern == "curl " || candidate.pattern == "wget ") &&
				!strings.Contains(lower, "| sh") &&
				!strings.Contains(lower, "| bash") &&
				!strings.Contains(lower, "| zsh") &&
				!strings.Contains(lower, "iex") {
				continue
			}
			return commandRisk{dangerous: true, reason: candidate.reason}
		}
	}
	return commandRisk{}
}

func isDestructiveCommand(cmd string) bool {
	return classifyCommand(cmd).dangerous
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.state == stateLoading {
		cmds = append(cmds, m.spinner.Tick, m.sendPrompt())
	} else if !m.modelsReady || m.refreshModels {
		cmds = append(cmds, m.loadModels())
	}
	return tea.Batch(cmds...)
}

func (m model) loadModels() tea.Cmd {
	return func() tea.Msg {
		models, err := m.copilot.listModels(context.Background())
		return modelsLoadedMsg{models: models, err: err}
	}
}

func (m model) sendPrompt() tea.Cmd {
	prompt := m.prompt
	modelName := m.selectedModel()

	updates := make(chan streamUpdate, 100)
	go m.copilot.ask(context.Background(), prompt, modelName, m.shell, updates)

	return listenForUpdates(updates)
}

func listenForUpdates(updates <-chan streamUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return streamDoneMsg{}
		}
		if update.err != nil {
			return streamErrMsg{err: update.err}
		}
		if update.done {
			return streamDoneMsg{final: update.final}
		}

		// Coalesce all deltas already waiting in the buffer so fast token bursts
		// trigger one render instead of one full terminal redraw per token.
		var delta strings.Builder
		delta.WriteString(update.delta)
		for {
			select {
			case next, ok := <-updates:
				if !ok {
					return streamDoneMsg{}
				}
				if next.err != nil {
					return streamErrMsg{err: next.err}
				}
				if next.done {
					return streamDoneMsg{final: next.final}
				}
				delta.WriteString(next.delta)
			default:
				return streamDeltaMsg{delta: delta.String(), updates: updates}
			}
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// On Windows, WindowSizeMsg may report buffer width instead of
		// visible window width. Use the actual console window width.
		if w := consoleWindowWidth(); w > 0 && w < m.width {
			m.width = w
		}
		inputWidth := m.innerWidth()
		m.textInput.Width = inputWidth
		m.refineInput.Width = inputWidth
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateInput:
			switch msg.String() {
			case "ctrl+c", "esc":
				m.copilot.stop()
				return m, tea.Quit
			case "tab":
				if len(m.models) > 0 {
					m.modelIndex = (m.modelIndex + 1) % len(m.models)
					m.saveSelectedModel()
				}
				return m, nil
			case "shift+tab":
				if len(m.models) > 0 {
					m.modelIndex = (m.modelIndex - 1 + len(m.models)) % len(m.models)
					m.saveSelectedModel()
				}
				return m, nil
			case "enter":
				input := strings.TrimSpace(m.textInput.Value())
				if input == "" {
					return m, nil
				}
				m.prompt = input
				m.err = nil
				m.state = stateLoading
				m.streaming = ""
				return m, tea.Batch(m.spinner.Tick, m.sendPrompt())
			}
		case stateLoading:
			if msg.String() == "ctrl+c" || msg.String() == "esc" {
				m.copilot.stop()
				return m, tea.Quit
			}
		case stateResult:
			switch msg.String() {
			case "up":
				if len(m.candidates) > 1 {
					m.selectedIdx = (m.selectedIdx - 1 + len(m.candidates)) % len(m.candidates)
				}
				return m, nil
			case "down", "tab":
				if len(m.candidates) > 1 {
					m.selectedIdx = (m.selectedIdx + 1) % len(m.candidates)
				}
				return m, nil
			case "shift+tab":
				if len(m.candidates) > 1 {
					m.selectedIdx = (m.selectedIdx - 1 + len(m.candidates)) % len(m.candidates)
				}
				return m, nil
			case "enter":
				refineText := strings.TrimSpace(m.refineInput.Value())
				if refineText != "" {
					// Iterate: send the new prompt
					m.prompt = refineText
					m.refineInput.Reset()
					m.err = nil
					m.state = stateLoading
					m.streaming = ""
					return m, tea.Batch(m.spinner.Tick, m.sendPrompt())
				}
				// Empty input: insert the selected command
				m.exitAction = actionInsert
				m.copilot.stop()
				return m, tea.Quit
			case "ctrl+r":
				// Run immediately — with safety check for destructive commands
				if isDestructiveCommand(m.selectedCommand()) {
					m.state = stateConfirmRun
					return m, nil
				}
				m.exitAction = actionRun
				m.copilot.stop()
				return m, tea.Quit
			case "ctrl+y":
				m.exitAction = actionCopy
				m.copilot.stop()
				return m, tea.Quit
			case "ctrl+e":
				m.textInput.SetValue(m.prompt)
				m.textInput.CursorEnd()
				m.refineInput.Blur()
				m.textInput.Focus()
				m.state = stateInput
				return m, textinput.Blink
			case "ctrl+c", "esc":
				m.copilot.stop()
				return m, tea.Quit
			}
		case stateConfirmRun:
			switch msg.String() {
			case "y", "Y":
				m.exitAction = actionRun
				m.copilot.stop()
				return m, tea.Quit
			case "n", "N", "esc":
				m.state = stateResult
				return m, nil
			}
		case stateError:
			switch msg.String() {
			case "r", "enter":
				m.err = nil
				m.streaming = ""
				if strings.TrimSpace(m.prompt) == "" {
					m.state = stateInput
					m.textInput.Focus()
					return m, textinput.Blink
				}
				m.state = stateLoading
				return m, tea.Batch(m.spinner.Tick, m.sendPrompt())
			case "ctrl+c", "esc", "q":
				m.copilot.stop()
				return m, tea.Quit
			}
		}

	case modelsLoadedMsg:
		m.modelsReady = true
		m.refreshModels = false
		if msg.err != nil {
			m.modelWarning = "Model list unavailable; using Copilot's default model"
			return m, nil
		}
		m.modelWarning = ""
		m.models = msg.models
		if len(m.models) > 0 {
			m.modelIndex = 0
			// Restore last-used model
			if m.preferredModel != "" {
				for i, name := range m.models {
					if name == m.preferredModel {
						m.modelIndex = i
						break
					}
				}
			}
			if err := updateConfig(func(cfg *config) {
				cfg.Models = append([]string(nil), m.models...)
				cfg.ModelsUpdatedAt = time.Now().Unix()
				if cfg.LastModel == "" {
					cfg.LastModel = m.preferredModel
				}
			}); err != nil {
				m.modelWarning = "Models loaded, but the local cache could not be saved"
			}
		}
		return m, nil

	case streamDeltaMsg:
		m.streaming += msg.delta
		return m, listenForUpdates(msg.updates)

	case streamDoneMsg:
		raw := strings.TrimSpace(msg.final)
		if raw == "" {
			raw = strings.TrimSpace(m.streaming)
		}
		if raw == "" {
			m.err = errors.New("Copilot returned an empty response")
			m.state = stateError
			return m, nil
		}
		parsed := parseResponse(raw)
		m.explanation = parsed.Explanation
		m.candidates = parsed.Candidates
		if len(m.candidates) == 0 {
			m.err = errors.New("Copilot did not return a runnable command")
			m.state = stateError
			return m, nil
		}
		m.selectedIdx = 0
		m.state = stateResult
		m.refineInput.Focus()
		return m, textinput.Blink

	case streamErrMsg:
		m.err = msg.err
		m.state = stateError
		return m, nil

	case spinner.TickMsg:
		if m.state == stateLoading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	if m.state == stateInput {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	if m.state == stateResult {
		var cmd tea.Cmd
		m.refineInput, cmd = m.refineInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) panelWidth() int {
	if m.width <= 0 {
		return 64
	}
	// Lip Gloss adds two columns of padding and two border columns outside
	// the configured width.
	width := m.width - 4
	if width > 80 {
		return 80
	}
	if width < 1 {
		return 1
	}
	return width
}

func (m model) innerWidth() int {
	return m.panelWidth()
}

func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}

	var wrapped []string
	for _, sourceLine := range strings.Split(text, "\n") {
		if sourceLine == "" {
			wrapped = append(wrapped, "")
			continue
		}

		var line strings.Builder
		lineWidth := 0
		for _, r := range sourceLine {
			runeWidth := runewidth.RuneWidth(r)
			if lineWidth > 0 && lineWidth+runeWidth > width {
				wrapped = append(wrapped, line.String())
				line.Reset()
				lineWidth = 0
			}
			line.WriteRune(r)
			lineWidth += runeWidth
		}
		wrapped = append(wrapped, line.String())
	}
	return strings.Join(wrapped, "\n")
}

func renderCommand(command string, width int, selected bool) string {
	prefix := "  "
	style := unselectedCmdStyle
	if selected {
		prefix = "▸ "
		style = selectedCmdStyle
	}

	available := width - runewidth.StringWidth(prefix)
	if available < 1 {
		available = 1
	}
	lines := strings.Split(wrapText(command, available), "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = "  " + line
		}
	}
	return style.Render(strings.Join(lines, "\n"))
}

func renderStreaming(raw string, width int) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	var rendered []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "EXPLANATION:"):
			explanation := strings.TrimSpace(strings.TrimPrefix(trimmed, "EXPLANATION:"))
			rendered = append(rendered, helpStyle.Render(wrapText(explanation, width)))
		case strings.HasPrefix(trimmed, "COMMAND:"):
			command := strings.TrimSpace(strings.TrimPrefix(trimmed, "COMMAND:"))
			rendered = append(rendered, renderCommand(command, width, true))
		case trimmed != "":
			rendered = append(rendered, wrapText(trimmed, width))
		}
	}
	return strings.Join(rendered, "\n")
}

func (m model) View() string {
	var content string

	innerWidth := m.innerWidth()

	switch m.state {
	case stateInput:
		modelTag := helpStyle.Render("…")
		if modelName := m.selectedModel(); modelName != "" {
			modelTag = modelTagStyle.Render(modelName)
			if reasoningEffort(modelName) == "none" {
				modelTag += " " + helpStyle.Render("· no reasoning")
			}
		}
		content = titleStyle.Render("✦ cpt") + " " + modelTag + " " + helpStyle.Render("tab↹ model") + "\n" +
			m.textInput.View()
		if m.modelWarning != "" {
			content += "\n" + warningStyle.Render(wrapText(m.modelWarning, innerWidth))
		}

	case stateLoading:
		preview := renderStreaming(m.streaming, innerWidth)
		if preview == "" {
			preview = m.spinner.View() + " Thinking..."
		}
		header := titleStyle.Render("✦ cpt") + " " + promptStyle.Render(wrapText(m.prompt, innerWidth-6))
		content = header + "\n" + preview
		if m.modelWarning != "" {
			content += "\n" + warningStyle.Render(wrapText(m.modelWarning, innerWidth))
		}

	case stateResult:
		if m.explanation != "" {
			content += helpStyle.Render(wrapText(m.explanation, innerWidth)) + "\n"
		}
		for i, c := range m.candidates {
			content += renderCommand(c.Command, innerWidth, i == m.selectedIdx)
			if i < len(m.candidates)-1 {
				content += "\n"
			}
		}
		content += "\n\n" + m.refineInput.View() + "\n"
		var hints string
		if m.bare {
			if len(m.candidates) > 1 {
				hints = "enter copy • ↑↓ choose • type refine • ctrl+e edit • esc quit"
			} else {
				hints = "enter copy • type refine • ctrl+e edit • esc quit"
			}
		} else {
			if len(m.candidates) > 1 {
				hints = "enter accept • ctrl+r run • ctrl+y copy • ↑↓ choose • type refine"
			} else {
				hints = "enter accept • ctrl+r run • ctrl+y copy • type refine • esc quit"
			}
		}
		content += helpStyle.Render(wrapText(hints, innerWidth))

	case stateConfirmRun:
		risk := classifyCommand(m.selectedCommand())
		content = warningStyle.Render("⚠ Confirmation required — "+risk.reason) + "\n" +
			renderCommand(m.selectedCommand(), innerWidth, true) + "\n\n" +
			helpStyle.Render("Run it? (y/n)")

	case stateError:
		errMsg := m.err.Error()
		content = titleStyle.Render("✦ cpt") + "\n\n" +
			errorStyle.Render(wrapText("Error: "+errMsg, innerWidth)) + "\n\n"
		// Provide actionable guidance based on common failure modes
		if strings.Contains(errMsg, "copilot") || strings.Contains(errMsg, "start") || strings.Contains(errMsg, "token") || strings.Contains(errMsg, "auth") {
			content += errorHintStyle.Render("Make sure GitHub Copilot CLI is installed and you're logged in:") + "\n" +
				errorHintStyle.Render("  1. Install:  gh extension install github/gh-copilot") + "\n" +
				errorHintStyle.Render("  2. Log in:   gh auth login") + "\n"
		} else {
			content += errorHintStyle.Render("Something went wrong. Check your network connection") + "\n" +
				errorHintStyle.Render("and ensure GitHub Copilot is available.") + "\n"
		}
		content += "\n" + helpStyle.Render("enter retry • esc quit")
	}

	style := panelStyle
	if m.state == stateError {
		style = style.BorderForeground(coral)
	}
	style = style.Width(m.panelWidth())
	return style.Render(content) + "\n"
}
