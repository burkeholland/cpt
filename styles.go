package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// renderer targets the TTY so colors work even when stdout is piped by a shell widget
var renderer = func() *lipgloss.Renderer {
	tty, err := openTTYOut()
	if err != nil {
		return lipgloss.DefaultRenderer()
	}
	r := lipgloss.NewRenderer(tty,
		termenv.WithProfile(termenv.TrueColor),
		termenv.WithUnsafe(), // don't query terminal — avoids OSC escape leaks
	)
	r.SetHasDarkBackground(true)
	return r
}()

var (
	// PostrBoard palette: Coral #ff7f50, Azure #0ea5e9, Sage #84cc16
	coral  = lipgloss.AdaptiveColor{Light: "#e06030", Dark: "#ff7f50"}
	azure  = lipgloss.AdaptiveColor{Light: "#0284c7", Dark: "#0ea5e9"}
	sage   = lipgloss.AdaptiveColor{Light: "#65a30d", Dark: "#84cc16"}
	subtle = lipgloss.AdaptiveColor{Light: "#4b5563", Dark: "#6b7280"}

	panelStyle = renderer.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(azure).
			Padding(0, 1)

	titleStyle    = renderer.NewStyle().Bold(true).Foreground(coral)
	promptStyle   = renderer.NewStyle().Foreground(subtle).Italic(true)
	helpStyle     = renderer.NewStyle().Foreground(subtle)
	modelTagStyle = renderer.NewStyle().Foreground(azure)
	spinnerStyle  = renderer.NewStyle().Foreground(coral)

	selectedCmdStyle = renderer.NewStyle().
				Bold(true).
				Foreground(sage)

	unselectedCmdStyle = renderer.NewStyle().
				Foreground(subtle)

	errorStyle = renderer.NewStyle().
			Foreground(coral).
			Bold(true)

	errorHintStyle = renderer.NewStyle().
			Foreground(subtle)
)
