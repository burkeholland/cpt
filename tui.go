package main

import (
	"context"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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

type streamDoneMsg struct{}
type streamErrMsg struct{ err error }

type model struct {
	state       viewState
	textInput   textinput.Model
	refineInput textinput.Model
	spinner     spinner.Model
	copilot     *copilotClient
	models      []string
	modelIndex  int
	candidates  []CommandCandidate
	selectedIdx int
	explanation string
	streaming   string
	err         error
	exitAction  exitActionType
	prompt      string
	shell       string
	bare        bool
	width       int
	height      int
}

func newModel(inlinePrompt string, bare bool) model {
	ti := textinput.New()
	ti.Placeholder = "Ask anything... (e.g., kill process on port 3000)"
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

	return model{
		state:       stateInput,
		textInput:   ti,
		refineInput: ri,
		spinner:     s,
		copilot:     newCopilotClient(),
		prompt:      inlinePrompt,
		shell:       detectShell(),
		bare:        bare,
	}
}

// selectedCommand returns the currently selected command text.
func (m model) selectedCommand() string {
	if len(m.candidates) == 0 {
		return ""
	}
	return m.candidates[m.selectedIdx].Command
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
				resp.Explanation = strings.TrimSpace(strings.TrimPrefix(trimmed, "EXPLANATION:"))
			} else if strings.HasPrefix(trimmed, "COMMAND:") {
				cmd := strings.TrimSpace(strings.TrimPrefix(trimmed, "COMMAND:"))
				if cmd != "" {
					resp.Candidates = append(resp.Candidates, CommandCandidate{Command: cmd})
				}
			}
		}
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

		resp.Candidates = append(resp.Candidates, CommandCandidate{Command: trimmed})
	}

	// Handle unclosed fence
	if inFence && len(fenceLines) > 0 {
		cmd := strings.TrimSpace(strings.Join(fenceLines, "\n"))
		if cmd != "" {
			resp.Candidates = append(resp.Candidates, CommandCandidate{Command: cmd})
		}
	}

	return resp
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

// isDestructiveCommand checks if a command looks dangerous enough to require confirmation before running.
func isDestructiveCommand(cmd string) bool {
	lower := strings.ToLower(cmd)
	patterns := []string{
		// POSIX
		"rm -rf", "rm -r ", "rm -fr",
		"sudo ",
		"chmod -r", "chown -r",
		"kill -9", "killall",
		"dd ",
		"mkfs",
		"> /dev/",
		"docker system prune",
		"kubectl delete",
		":(){", "fork bomb",
		// Windows / PowerShell
		"remove-item", "del /s", "rd /s",
		"format-volume", "clear-disk", "remove-partition",
		"stop-computer", "restart-computer",
		"stop-process -force",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.loadModels(),
	)
}

func (m model) loadModels() tea.Cmd {
	return func() tea.Msg {
		models, err := m.copilot.listModels(context.Background())
		return modelsLoadedMsg{models: models, err: err}
	}
}

func (m model) sendPrompt() tea.Cmd {
	prompt := m.prompt
	modelName := ""
	if len(m.models) > 0 {
		modelName = m.models[m.modelIndex]
	}

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
			return streamDoneMsg{}
		}
		return streamDeltaMsg{delta: update.delta, updates: updates}
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
					saveConfig(config{LastModel: m.models[m.modelIndex]})
				}
				return m, nil
			case "shift+tab":
				if len(m.models) > 0 {
					m.modelIndex = (m.modelIndex - 1 + len(m.models)) % len(m.models)
					saveConfig(config{LastModel: m.models[m.modelIndex]})
				}
				return m, nil
			case "enter":
				input := strings.TrimSpace(m.textInput.Value())
				if input == "" {
					return m, nil
				}
				m.prompt = input
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
			case "down":
				if len(m.candidates) > 1 {
					m.selectedIdx = (m.selectedIdx + 1) % len(m.candidates)
				}
				return m, nil
			case "enter":
				refineText := strings.TrimSpace(m.refineInput.Value())
				if refineText != "" {
					// Iterate: send the new prompt
					m.prompt = refineText
					m.refineInput.Reset()
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
			if msg.String() == "ctrl+c" || msg.String() == "esc" || msg.String() == "q" || msg.String() == "enter" {
				m.copilot.stop()
				return m, tea.Quit
			}
		}

	case modelsLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.state = stateError
			return m, nil
		}
		m.models = msg.models
		if len(m.models) > 0 {
			m.modelIndex = 0
			// Restore last-used model
			cfg := loadConfig()
			if cfg.LastModel != "" {
				for i, name := range m.models {
					if name == cfg.LastModel {
						m.modelIndex = i
						break
					}
				}
			}
		}
		if m.prompt != "" && m.state == stateInput {
			m.state = stateLoading
			return m, tea.Batch(m.spinner.Tick, m.sendPrompt())
		}
		return m, nil

	case streamDeltaMsg:
		m.streaming += msg.delta
		return m, listenForUpdates(msg.updates)

	case streamDoneMsg:
		raw := strings.TrimSpace(m.streaming)
		parsed := parseResponse(raw)
		m.explanation = parsed.Explanation
		m.candidates = parsed.Candidates
		if len(m.candidates) == 0 {
			m.candidates = []CommandCandidate{{Command: raw}}
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

func (m model) View() string {
	var content string

	// Inner width accounts for border (2) and padding (2)
	innerWidth := m.width - 4
	if innerWidth <= 0 {
		innerWidth = 60
	}

	switch m.state {
	case stateInput:
		modelTag := helpStyle.Render("…")
		if len(m.models) > 0 {
			modelTag = modelTagStyle.Render(m.models[m.modelIndex])
		}
		content = titleStyle.Render("✦ cpt") + " " + modelTag + " " + helpStyle.Render("tab↹ model") + "\n" +
			m.textInput.View()

	case stateLoading:
		preview := m.streaming
		if preview == "" {
			preview = m.spinner.View() + " Thinking..."
		} else {
			// Strip EXPLANATION:/COMMAND: prefixes during streaming for cleaner display
			var cleaned []string
			for _, line := range strings.Split(preview, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "EXPLANATION:") {
					cleaned = append(cleaned, helpStyle.Render(strings.TrimSpace(strings.TrimPrefix(trimmed, "EXPLANATION:"))))
				} else if strings.HasPrefix(trimmed, "COMMAND:") {
					cleaned = append(cleaned, selectedCmdStyle.Render("▸ "+strings.TrimSpace(strings.TrimPrefix(trimmed, "COMMAND:"))))
				} else if trimmed != "" {
					cleaned = append(cleaned, trimmed)
				}
			}
			if len(cleaned) > 0 {
				preview = strings.Join(cleaned, "\n")
			}
		}
		header := titleStyle.Render("✦ cpt") + " " + promptStyle.Render(m.prompt)
		content = renderer.NewStyle().MaxWidth(innerWidth).Render(header) + "\n" +
			preview

	case stateResult:
		if m.explanation != "" {
			content += helpStyle.Render(m.explanation) + "\n"
		}
		for i, c := range m.candidates {
			if i == m.selectedIdx {
				content += selectedCmdStyle.Render("▸ " + c.Command)
			} else {
				content += unselectedCmdStyle.Render("  " + c.Command)
			}
			if i < len(m.candidates)-1 {
				content += "\n"
			}
		}
		content += "\n\n" + m.refineInput.View() + "\n"
		var hints string
		if m.bare {
			if len(m.candidates) > 1 {
				hints = "enter copy • ↑↓ alternatives • type to refine • esc quit"
			} else {
				hints = "enter copy • type to refine • esc quit"
			}
		} else {
			if len(m.candidates) > 1 {
				hints = "enter accept • ctrl+r run • ↑↓ alternatives • type to refine • esc quit"
			} else {
				hints = "enter accept • ctrl+r run • type to refine • esc quit"
			}
		}
		content += helpStyle.Render(hints)

	case stateConfirmRun:
		content = errorStyle.Render("⚠ This command looks destructive:") + "\n" +
			selectedCmdStyle.Render("▸ "+m.selectedCommand()) + "\n\n" +
			helpStyle.Render("Run it? (y/n)")

	case stateError:
		errMsg := m.err.Error()
		content = titleStyle.Render("✦ cpt") + "\n\n" +
			errorStyle.MaxWidth(innerWidth).Render("Error: "+errMsg) + "\n\n"
		// Provide actionable guidance based on common failure modes
		if strings.Contains(errMsg, "copilot") || strings.Contains(errMsg, "start") || strings.Contains(errMsg, "token") || strings.Contains(errMsg, "auth") {
			content += errorHintStyle.Render("Make sure GitHub Copilot CLI is installed and you're logged in:") + "\n" +
				errorHintStyle.Render("  1. Install:  gh extension install github/gh-copilot") + "\n" +
				errorHintStyle.Render("  2. Log in:   gh auth login") + "\n"
		} else {
			content += errorHintStyle.Render("Something went wrong. Check your network connection") + "\n" +
				errorHintStyle.Render("and ensure GitHub Copilot is available.") + "\n"
		}
		content += "\n" + helpStyle.Render("press any key to exit")
	}

	style := panelStyle
	if m.state == stateError {
		style = style.BorderForeground(coral)
	}
	// Always use a fixed panel width so all frames are the same size.
	// This prevents ghost characters from previous wider frames on Windows.
	style = style.Width(62)
	return style.Render(content) + "\n"
}
