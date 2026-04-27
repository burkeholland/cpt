package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("cpt " + version)
		return
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--setup":
			printSetup()
			return
		case "--install":
			installWidget()
			return
		}
	}

	var inlinePrompt string
	if len(os.Args) > 1 {
		inlinePrompt = strings.Join(os.Args[1:], " ")
	}

	m := newModel(inlinePrompt)

	// Render TUI directly to TTY so colors work even when
	// stdout is captured by a shell widget
	ttyOut, err := openTTYOut()
	if err != nil {
		ttyOut = os.Stderr // fallback
	}
	p := tea.NewProgram(m, tea.WithOutput(ttyOut))
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Clear the inline TUI from the terminal so it disappears on dismiss
	fm := finalModel.(model)
	lastView := fm.View()
	lineCount := strings.Count(lastView, "\n")
	if lineCount > 0 {
		// Move cursor up and clear each line the TUI occupied
		for i := 0; i < lineCount; i++ {
			fmt.Fprintf(ttyOut, "\033[A\033[2K")
		}
		fmt.Fprintf(ttyOut, "\r")
	}

	switch fm.exitAction {
	case actionInsert:
		// Print to stdout — the shell widget captures this via $(cpt)
		// and places it on the prompt ready to execute
		fmt.Print(fm.result)
	case actionCopy:
		if err := copyToClipboard(fm.result); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to copy: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Copied to clipboard!")
	}
}

func detectShell() string {
	// On Unix, trust $SHELL first
	if runtime.GOOS != "windows" {
		shell := os.Getenv("SHELL")
		base := filepath.Base(shell)
		switch base {
		case "zsh":
			return "zsh"
		case "bash":
			return "bash"
		case "fish":
			return "fish"
		}
		return "zsh" // default on Unix
	}
	// On Windows, default to PowerShell
	return "powershell"
}

func shellRCPath(shell string) string {
	home, _ := os.UserHomeDir()
	switch shell {
	case "bash":
		return filepath.Join(home, ".bashrc")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	case "powershell":
		// Resolve profile path dynamically via PowerShell
		for _, ps := range []string{"pwsh", "powershell"} {
			if psPath, err := exec.LookPath(ps); err == nil {
				out, err := exec.Command(psPath, "-NoProfile", "-Command", "$PROFILE.CurrentUserCurrentHost").Output()
				if err == nil {
					if p := strings.TrimSpace(string(out)); p != "" {
						return p
					}
				}
			}
		}
		// Fallback
		docs := filepath.Join(home, "Documents")
		return filepath.Join(docs, "PowerShell", "Microsoft.PowerShell_profile.ps1")
	default:
		return filepath.Join(home, ".zshrc")
	}
}

// shellEscape quotes a path safely for the target shell.
func shellEscape(path, shell string) string {
	switch shell {
	case "powershell":
		// PowerShell: single-quote, doubling any embedded single quotes
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	default:
		// POSIX shells (bash/zsh/fish): single-quote, escape embedded quotes
		return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
	}
}

func installWidget() {
	shell := detectShell()
	rcPath := shellRCPath(shell)

	// Resolve the absolute path of this binary
	binPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not determine binary path: %v\n", err)
		os.Exit(1)
	}
	binPath, _ = filepath.EvalSymlinks(binPath)

	// Check if already installed
	existing, _ := os.ReadFile(rcPath)
	if strings.Contains(string(existing), "cpt-widget") || strings.Contains(string(existing), "cpt-readline") || strings.Contains(string(existing), "Invoke-Cpt") {
		fmt.Fprintf(os.Stderr, "✓ cpt is already installed in %s\n", rcPath)
		if shell == "powershell" {
			fmt.Fprintf(os.Stderr, "  Restart PowerShell or run: . $PROFILE\n")
		} else {
			fmt.Fprintf(os.Stderr, "  Restart your shell or run: source %s\n", rcPath)
		}
		return
	}

	// Build widget with shell-safe path
	shellBin := shellEscape(binPath, shell)
	var widget string
	switch shell {
	case "bash":
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
cpt-readline() { local cmd; cmd=$(%s 2>/dev/tty); READLINE_LINE="$cmd"; READLINE_POINT=${#cmd}; }
bind -x '"\C-k": cpt-readline'
`, shellBin)
	case "fish":
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
function cpt-widget
    commandline (%s 2>/dev/tty)
    commandline -f repaint
end
bind \ck cpt-widget
`, shellBin)
		os.MkdirAll(filepath.Dir(rcPath), 0755)
	case "powershell":
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
if (Get-Command Set-PSReadLineKeyHandler -ErrorAction SilentlyContinue) {
    function Invoke-Cpt {
        $result = & %s
        [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt()
        if ($result) {
            [Microsoft.PowerShell.PSConsoleReadLine]::Insert($result)
        }
    }
    Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }
}
`, shellBin)
		os.MkdirAll(filepath.Dir(rcPath), 0755)
	default:
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
cpt-widget() { BUFFER=$(%s 2>/dev/tty); CURSOR=$#BUFFER; zle redisplay }
zle -N cpt-widget
bindkey '^K' cpt-widget
`, shellBin)
	}

	f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not write to %s: %v\n", rcPath, err)
		os.Exit(1)
	}
	defer f.Close()

	if _, err := f.WriteString(widget); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing to %s: %v\n", rcPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "✓ Installed cpt widget in %s\n", rcPath)
	fmt.Fprintf(os.Stderr, "  Press Ctrl+K to launch cpt from anywhere!\n")
	if shell == "powershell" {
		fmt.Fprintf(os.Stderr, "  Restart PowerShell or run: . $PROFILE\n")
	} else {
		fmt.Fprintf(os.Stderr, "  Restart your shell or run: source %s\n", rcPath)
	}
}

func printSetup() {
	fmt.Println("Run `cpt --install` to auto-install, or add manually:")
	fmt.Println()
	fmt.Println("  # Zsh (~/.zshrc)")
	fmt.Println(`  cpt-widget() { BUFFER=$(cpt 2>/dev/tty); CURSOR=$#BUFFER; zle redisplay }`)
	fmt.Println(`  zle -N cpt-widget`)
	fmt.Println(`  bindkey '^K' cpt-widget`)
	fmt.Println()
	fmt.Println("  # Bash (~/.bashrc)")
	fmt.Println(`  cpt-readline() { local cmd; cmd=$(cpt 2>/dev/tty); READLINE_LINE="$cmd"; READLINE_POINT=${#cmd}; }`)
	fmt.Println(`  bind -x '"\C-k": cpt-readline'`)
	fmt.Println()
	fmt.Println("  # Fish (~/.config/fish/config.fish)")
	fmt.Println(`  function cpt-widget; commandline (cpt 2>/dev/tty); commandline -f repaint; end`)
	fmt.Println(`  bind \ck cpt-widget`)
	fmt.Println()
	fmt.Println("  # PowerShell ($PROFILE)")
	fmt.Println(`  function Invoke-Cpt { $result = & cpt; [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt(); if ($result) { [Microsoft.PowerShell.PSConsoleReadLine]::Insert($result) } }`)
	fmt.Println(`  Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }`)
}
