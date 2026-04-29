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

// isStdoutPiped returns true when stdout is a pipe (inside shell widget),
// false when stdout is a TTY (running cpt directly).
func isStdoutPiped() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

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

	bare := !isStdoutPiped()
	m := newModel(inlinePrompt, bare)

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
		cmd := fm.selectedCommand()
		if isStdoutPiped() {
			// Inside shell widget $(cpt) — print to stdout for capture
			fmt.Print(cmd)
		} else {
			// Running bare (cpt typed directly) — copy to clipboard
			if err := copyToClipboard(cmd); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to copy: %v\n", err)
				fmt.Print(cmd) // fallback: print to stdout
			} else {
				fmt.Fprintf(ttyOut, "✓ Copied to clipboard\n")
			}
		}
	case actionRun:
		cmd := fm.selectedCommand()
		if isStdoutPiped() {
			// Inside shell widget — print + exit 42 signals "execute"
			fmt.Print(cmd)
			os.Exit(exitCodeRun)
		} else {
			// Running bare — copy to clipboard with note
			if err := copyToClipboard(cmd); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to copy: %v\n", err)
				fmt.Print(cmd)
			} else {
				fmt.Fprintf(ttyOut, "✓ Copied to clipboard — paste and run\n")
			}
		}
	case actionCopy:
		if err := copyToClipboard(fm.selectedCommand()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to copy: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(ttyOut, "✓ Copied to clipboard\n")
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
	// On Windows, check for Git Bash/MSYS2/Cygwin environments
	if shell := os.Getenv("SHELL"); shell != "" {
		base := filepath.Base(shell)
		switch base {
		case "bash", "bash.exe":
			return "bash"
		case "zsh", "zsh.exe":
			return "zsh"
		case "fish", "fish.exe":
			return "fish"
		}
	}
	if os.Getenv("MSYSTEM") != "" || os.Getenv("MINGW_PREFIX") != "" {
		return "bash"
	}
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
cpt-readline() {
    local cmd status
    cmd=$(%s 2>/dev/tty)
    status=$?
    if [ "$status" -eq 42 ]; then
        READLINE_LINE="$cmd"
        READLINE_POINT=${#cmd}
        # Bash bind -x cannot auto-execute; user must press Enter
    elif [ "$status" -eq 0 ] && [ -n "$cmd" ]; then
        READLINE_LINE="$cmd"
        READLINE_POINT=${#cmd}
    fi
}
bind -x '"\C-k": cpt-readline'
`, shellBin)
	case "fish":
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
function cpt-widget
    set -l cmd (%s 2>/dev/tty)
    set -l cpt_status $status
    if test $cpt_status -eq 42
        commandline $cmd
        commandline -f execute
    else if test $cpt_status -eq 0 -a -n "$cmd"
        commandline $cmd
    end
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
        $result = (& %s) -join [Environment]::NewLine
        $exitCode = $LASTEXITCODE
        if ($result.Length -gt 0) {
            $line = $null
            $cursor = $null
            [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$line, [ref]$cursor)
            [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, $line.Length, $result)
            if ($exitCode -eq 42) {
                [Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine()
            }
        }
    }
    Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }
}
`, shellBin)
		os.MkdirAll(filepath.Dir(rcPath), 0755)
	default:
		widget = fmt.Sprintf(`
# cpt - terminal copilot (Ctrl+K)
cpt-widget() {
    local cmd
    cmd=$(%s 2>/dev/tty)
    local cpt_status=$?
    if [[ $cpt_status -eq 42 ]]; then
        BUFFER="$cmd"
        zle accept-line
    elif [[ $cpt_status -eq 0 ]] && [[ -n "$cmd" ]]; then
        BUFFER="$cmd"
        CURSOR=$#BUFFER
        zle redisplay
    fi
}
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
	fmt.Println(`  cpt-widget() {`)
	fmt.Println(`      local cmd`)
	fmt.Println(`      cmd=$(cpt 2>/dev/tty)`)
	fmt.Println(`      local cpt_status=$?`)
	fmt.Println(`      if [[ $cpt_status -eq 42 ]]; then`)
	fmt.Println(`          BUFFER="$cmd"`)
	fmt.Println(`          zle accept-line`)
	fmt.Println(`      elif [[ $cpt_status -eq 0 ]] && [[ -n "$cmd" ]]; then`)
	fmt.Println(`          BUFFER="$cmd"`)
	fmt.Println(`          CURSOR=$#BUFFER`)
	fmt.Println(`          zle redisplay`)
	fmt.Println(`      fi`)
	fmt.Println(`  }`)
	fmt.Println(`  zle -N cpt-widget`)
	fmt.Println(`  bindkey '^K' cpt-widget`)
	fmt.Println()
	fmt.Println("  # Bash (~/.bashrc)")
	fmt.Println(`  cpt-readline() {`)
	fmt.Println(`      local cmd status`)
	fmt.Println(`      cmd=$(cpt 2>/dev/tty)`)
	fmt.Println(`      status=$?`)
	fmt.Println(`      if [ "$status" -eq 42 ]; then`)
	fmt.Println(`          READLINE_LINE="$cmd"`)
	fmt.Println(`          READLINE_POINT=${#cmd}`)
	fmt.Println(`      elif [ "$status" -eq 0 ] && [ -n "$cmd" ]; then`)
	fmt.Println(`          READLINE_LINE="$cmd"`)
	fmt.Println(`          READLINE_POINT=${#cmd}`)
	fmt.Println(`      fi`)
	fmt.Println(`  }`)
	fmt.Println(`  bind -x '"\C-k": cpt-readline'`)
	fmt.Println()
	fmt.Println("  # Fish (~/.config/fish/config.fish)")
	fmt.Println(`  function cpt-widget`)
	fmt.Println(`      set -l cmd (cpt 2>/dev/tty)`)
	fmt.Println(`      set -l cpt_status $status`)
	fmt.Println(`      if test $cpt_status -eq 42`)
	fmt.Println(`          commandline $cmd`)
	fmt.Println(`          commandline -f execute`)
	fmt.Println(`      else if test $cpt_status -eq 0 -a -n "$cmd"`)
	fmt.Println(`          commandline $cmd`)
	fmt.Println(`      end`)
	fmt.Println(`      commandline -f repaint`)
	fmt.Println(`  end`)
	fmt.Println(`  bind \ck cpt-widget`)
	fmt.Println()
	fmt.Println("  # PowerShell ($PROFILE)")
	fmt.Println(`  function Invoke-Cpt {`)
	fmt.Println(`      $result = (& cpt) -join [Environment]::NewLine`)
	fmt.Println(`      $exitCode = $LASTEXITCODE`)
	fmt.Println(`      if ($result.Length -gt 0) {`)
	fmt.Println(`          $line = $null; $cursor = $null`)
	fmt.Println(`          [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$line, [ref]$cursor)`)
	fmt.Println(`          [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, $line.Length, $result)`)
	fmt.Println(`          if ($exitCode -eq 42) { [Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine() }`)
	fmt.Println(`      }`)
	fmt.Println(`  }`)
	fmt.Println(`  Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }`)
}
