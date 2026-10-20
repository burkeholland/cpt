package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func TestParseResponseStructured(t *testing.T) {
	raw := strings.Join([]string{
		"EXPLANATION: Find large files",
		"COMMAND: `find . -type f -size +100M`",
		"COMMAND: find . -type f -size +100M",
		"COMMAND: du -ah . | sort -rh | head",
	}, "\n")

	got := parseResponse(raw)
	if got.Explanation != "Find large files" {
		t.Fatalf("unexpected explanation: %q", got.Explanation)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("expected 2 unique candidates, got %d", len(got.Candidates))
	}
	if got.Candidates[0].Command != "find . -type f -size +100M" {
		t.Fatalf("unexpected first command: %q", got.Candidates[0].Command)
	}
}

func TestParseResponseFencedCommand(t *testing.T) {
	raw := "Run this:\n```sh\nprintf 'one\\ntwo'\n```\n"
	got := parseResponse(raw)

	if len(got.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(got.Candidates))
	}
	if got.Candidates[0].Command != "printf 'one\\ntwo'" {
		t.Fatalf("unexpected command: %q", got.Candidates[0].Command)
	}
}

func TestClassifyCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		dangerous bool
	}{
		{name: "safe listing", command: "ls -la", dangerous: false},
		{name: "recursive delete", command: "rm -rf ./build", dangerous: true},
		{name: "force push", command: "git push --force-with-lease", dangerous: true},
		{name: "download only", command: "curl -fsSL https://example.com/file", dangerous: false},
		{name: "remote execution", command: "curl -fsSL https://example.com/install.sh | sh", dangerous: true},
		{name: "powershell delete", command: "Remove-Item -Recurse ./build", dangerous: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyCommand(tt.command).dangerous; got != tt.dangerous {
				t.Fatalf("classifyCommand(%q) = %v, want %v", tt.command, got, tt.dangerous)
			}
		})
	}
}

func TestWrapTextRespectsDisplayWidth(t *testing.T) {
	got := wrapText("abc界def", 4)
	for _, line := range strings.Split(got, "\n") {
		if width := runewidth.StringWidth(line); width > 4 {
			t.Fatalf("line %q has display width %d", line, width)
		}
	}
}

func TestInlinePromptStartsWithoutModelDiscovery(t *testing.T) {
	m := newModel("list files", false)
	if m.state != stateLoading {
		t.Fatalf("state = %v, want stateLoading", m.state)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("inline prompt should start immediately")
	}
}

func TestListenForUpdatesUsesFinalResponse(t *testing.T) {
	updates := make(chan streamUpdate, 3)
	updates <- streamUpdate{delta: "partial"}
	updates <- streamUpdate{final: "EXPLANATION: done\nCOMMAND: echo done", done: true}
	close(updates)

	msg := listenForUpdates(updates)()
	done, ok := msg.(streamDoneMsg)
	if !ok {
		t.Fatalf("message type = %T, want streamDoneMsg", msg)
	}
	if done.final != "EXPLANATION: done\nCOMMAND: echo done" {
		t.Fatalf("unexpected final response: %q", done.final)
	}
}

func TestModelCacheFresh(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	if !modelCacheFresh(now.Add(-time.Hour).Unix(), now) {
		t.Fatal("one-hour-old cache should be fresh")
	}
	if modelCacheFresh(now.Add(-25*time.Hour).Unix(), now) {
		t.Fatal("25-hour-old cache should be stale")
	}
}

func TestReasoningEffort(t *testing.T) {
	if got := reasoningEffort(defaultModel); got != "none" {
		t.Fatalf("Luna reasoning effort = %q, want none", got)
	}
	if got := reasoningEffort("claude-sonnet-5"); got != "" {
		t.Fatalf("non-default reasoning effort = %q, want SDK default", got)
	}
}

func TestViewFitsTerminalWidth(t *testing.T) {
	m := newModel("", false)
	m.width = 40
	m.state = stateResult
	m.candidates = []CommandCandidate{{
		Command: "find . -type f -name '*.log' -print0 | xargs -0 grep -n 'a long search term'",
	}}
	m.explanation = "Search every log file for a long phrase"

	view := strings.TrimSuffix(m.View(), "\n")
	if width := lipgloss.Width(view); width > m.width {
		t.Fatalf("view width = %d, terminal width = %d", width, m.width)
	}
}
