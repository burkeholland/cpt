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
	stateError
)

type exitActionType int

const (
	actionNone exitActionType = iota
	actionInsert
	actionCopy
)

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
	result      string
	streaming   string
	err         error
	exitAction  exitActionType
	prompt      string
	shell       string
	width       int
	height      int
}

func newModel(inlinePrompt string) model {
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
	}
}

// parseCommands extracts runnable shell commands from the response.
// It handles: bare lines, ```-fenced blocks, and lines starting with $ prompts.
func parseCommands(raw string) []string {
	var commands []string
	lines := strings.Split(raw, "\n")

	inFence := false
	fencePattern := regexp.MustCompile("^```")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle code fence state
		if fencePattern.MatchString(trimmed) {
			inFence = !inFence
			continue
		}

		// Inside a code fence, take non-empty lines
		if inFence {
			if trimmed != "" {
				commands = append(commands, trimmed)
			}
			continue
		}

		// Outside fence: skip empty lines and markdown-like prose
		if trimmed == "" {
			continue
		}
		// Skip lines that look like prose (start with common prose patterns)
		if isProseOrMarkdown(trimmed) {
			continue
		}

		// Strip leading $ prompt
		if strings.HasPrefix(trimmed, "$ ") {
			trimmed = strings.TrimPrefix(trimmed, "$ ")
		}

		commands = append(commands, trimmed)
	}

	return commands
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
				// Empty input: insert the command
				m.exitAction = actionInsert
				m.copilot.stop()
				return m, tea.Quit
			case "ctrl+c", "esc":
				m.copilot.stop()
				return m, tea.Quit
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
		commands := parseCommands(raw)
		if len(commands) > 0 {
			m.result = commands[0]
		} else {
			m.result = raw
		}
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
		}
		header := titleStyle.Render("✦ cpt") + " " + promptStyle.Render(m.prompt)
		content = renderer.NewStyle().MaxWidth(innerWidth).Render(header) + "\n" +
			preview

	case stateResult:
		cmdText := "▸ " + m.result
		content = selectedCmdStyle.MaxWidth(innerWidth).Render(cmdText) + "\n\n" +
			m.refineInput.View() + "\n" +
			helpStyle.Render("enter accept • type to refine • esc quit")

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
