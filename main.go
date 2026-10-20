package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var version = "dev"

const (
	widgetStartMarker = "# >>> cpt terminal copilot >>>"
	widgetEndMarker   = "# <<< cpt terminal copilot <<<"
)

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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Println("cpt " + version)
			return
		case "--help", "-h":
			printHelp()
			return
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
	fm, ok := finalModel.(model)
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: unexpected terminal state")
		os.Exit(1)
	}
	lastView := fm.View()
	lineCount := lipgloss.Height(strings.TrimSuffix(lastView, "\n"))
	if lineCount > 0 {
		// Bubble Tea leaves the cursor below the inline renderer. Move back
		// through every visual row, including rows created by command wrapping.
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

	existing, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: could not read %s: %v\n", rcPath, err)
		os.Exit(1)
	}
	existingContent, err := removeInstalledWidget(string(existing))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not safely upgrade widget in %s: %v\n", rcPath, err)
		os.Exit(1)
	}

	// Build widget with shell-safe path
	shellBin := shellEscape(binPath, shell)
	var widget string
	switch shell {
	case "bash":
		widget = fmt.Sprintf(`
%s
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
%s
`, widgetStartMarker, shellBin, widgetEndMarker)
	case "fish":
		widget = fmt.Sprintf(`
%s
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
%s
`, widgetStartMarker, shellBin, widgetEndMarker)
	case "powershell":
		widget = fmt.Sprintf(`
%s
# cpt - terminal copilot (Ctrl+K)
if (Get-Command Set-PSReadLineKeyHandler -ErrorAction SilentlyContinue) {
    function Invoke-Cpt {
        $result = (& %s) -join [Environment]::NewLine
        $exitCode = $LASTEXITCODE
        [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt()
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
%s
`, widgetStartMarker, shellBin, widgetEndMarker)
	default:
		widget = fmt.Sprintf(`
%s
# cpt - terminal copilot (Ctrl+K)
cpt-widget() {
    local cmd cpt_status
    zle -I
    cmd=$(%s 2>/dev/tty)
    cpt_status=$?
    if [[ $cpt_status -eq 42 ]] && [[ -n "$cmd" ]]; then
        BUFFER="$cmd"
        CURSOR=${#BUFFER}
        zle accept-line
    elif [[ $cpt_status -eq 0 ]] && [[ -n "$cmd" ]]; then
        BUFFER="$cmd"
        CURSOR=${#BUFFER}
        zle reset-prompt
    fi
}
zle -N cpt-widget
bindkey '^K' cpt-widget
%s
`, widgetStartMarker, shellBin, widgetEndMarker)
	}

	if err := os.MkdirAll(filepath.Dir(rcPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not create %s: %v\n", filepath.Dir(rcPath), err)
		os.Exit(1)
	}

	finalContent := strings.TrimRight(existingContent, "\n")
	if finalContent != "" {
		finalContent += "\n"
	}
	finalContent += strings.TrimLeft(widget, "\n")
	if err := atomicWriteFile(rcPath, []byte(finalContent), 0644); err != nil {
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

func atomicWriteFile(path string, data []byte, defaultMode os.FileMode) error {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		target = resolved
	}

	mode := defaultMode
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".cpt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

func removeInstalledWidget(content string) (string, error) {
	lines := strings.Split(content, "\n")
	cleaned := make([]string, 0, len(lines))
	inWidget := false
	explicitBlock := false
	powerShellEndPending := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inWidget {
			switch {
			case trimmed == widgetStartMarker:
				inWidget = true
				explicitBlock = true
				continue
			case strings.Contains(trimmed, "# cpt - terminal copilot"):
				inWidget = true
				continue
			}
			cleaned = append(cleaned, line)
			continue
		}

		if explicitBlock {
			if trimmed == widgetEndMarker {
				inWidget = false
				explicitBlock = false
			}
			continue
		}

		if powerShellEndPending {
			if trimmed == "}" {
				inWidget = false
				powerShellEndPending = false
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "Set-PSReadLineKeyHandler"):
			powerShellEndPending = true
		case strings.Contains(trimmed, "bindkey") && strings.Contains(trimmed, "cpt-widget"):
			inWidget = false
		case strings.Contains(trimmed, `bind -x`) && strings.Contains(trimmed, "cpt-readline"):
			inWidget = false
		case strings.Contains(trimmed, `bind \ck cpt-widget`):
			inWidget = false
		}
	}

	if inWidget {
		return content, fmt.Errorf("found a cpt widget start marker without a complete end")
	}

	return strings.TrimRight(strings.Join(cleaned, "\n"), "\n"), nil
}

func printHelp() {
	fmt.Println(`cpt — GitHub Copilot for your terminal

Usage:
  cpt                     Open the interactive prompt
  cpt <request>           Generate a command immediately
  cpt --install           Install the Ctrl+K shell widget
  cpt --setup             Print manual shell setup
  cpt --version           Print the version

Inside cpt:
  enter       Accept the selected command
  ctrl+r      Run the selected command
  ctrl+y      Copy the selected command
  ctrl+e      Edit the request
  tab / ↑↓    Select models or command alternatives
  esc         Cancel`)
}

func printSetup() {
	fmt.Println("Run `cpt --install` to auto-install, or add manually:")
	fmt.Println()
	fmt.Println("  # Zsh (~/.zshrc)")
	fmt.Println(`  cpt-widget() {`)
	fmt.Println(`      local cmd cpt_status`)
	fmt.Println(`      zle -I`)
	fmt.Println(`      cmd=$(cpt 2>/dev/tty)`)
	fmt.Println(`      cpt_status=$?`)
	fmt.Println(`      if [[ $cpt_status -eq 42 ]] && [[ -n "$cmd" ]]; then`)
	fmt.Println(`          BUFFER="$cmd"`)
	fmt.Println(`          CURSOR=${#BUFFER}`)
	fmt.Println(`          zle accept-line`)
	fmt.Println(`      elif [[ $cpt_status -eq 0 ]] && [[ -n "$cmd" ]]; then`)
	fmt.Println(`          BUFFER="$cmd"`)
	fmt.Println(`          CURSOR=${#BUFFER}`)
	fmt.Println(`          zle reset-prompt`)
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
	fmt.Println(`      [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt()`)
	fmt.Println(`      if ($result.Length -gt 0) {`)
	fmt.Println(`          $line = $null; $cursor = $null`)
	fmt.Println(`          [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$line, [ref]$cursor)`)
	fmt.Println(`          [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, $line.Length, $result)`)
	fmt.Println(`          if ($exitCode -eq 42) { [Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine() }`)
	fmt.Println(`      }`)
	fmt.Println(`  }`)
	fmt.Println(`  Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }`)
}
