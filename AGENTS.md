# AGENTS.md

## Project Overview

**cpt** is a terminal copilot that adds GitHub Copilot chat to any shell. Users press **Ctrl+K**, describe what they want in plain English, and get a shell command back — ready to run.

- **Language:** Go
- **TUI framework:** [Bubble Tea](https://github.com/charmbracelet/bubbletea) (charmbracelet/bubbletea)
- **Styling:** [Lip Gloss](https://github.com/charmbracelet/lipgloss) (charmbracelet/lipgloss)
- **AI backend:** [GitHub Copilot SDK for Go](https://github.com/github/copilot-sdk)
- **Supported shells:** zsh, bash, fish, PowerShell
- **Supported platforms:** macOS, Linux, Windows (amd64, arm64)

## Repository Structure

| File | Purpose |
|---|---|
| `main.go` | Entry point, CLI flags, shell detection, widget installation |
| `tui.go` | Bubble Tea model, Update/View logic, command parsing |
| `copilot.go` | Copilot SDK client, streaming API calls |
| `styles.go` | Lip Gloss styles and color palette |
| `config.go` | User config (last-used model persistence) |
| `clipboard.go` | Cross-platform clipboard support |
| `tty_unix.go` | TTY output for Unix systems |
| `tty_windows.go` | TTY output for Windows |
| `index.html` | Landing page |
| `install.sh` | macOS/Linux install script |
| `install.ps1` | Windows PowerShell install script |

## Building

```sh
go build -o cpt .
# or
make build
```

## Testing Locally

```sh
make dev     # builds and runs ./cpt
./cpt        # run directly
./cpt "your prompt here"  # inline prompt mode
```

## Key Design Decisions

- The TUI renders **inline** below the current shell prompt (not in an alternate screen). On dismiss, ANSI escape codes erase the rendered lines so the terminal is clean.
- TUI output is written to the TTY directly (`openTTYOut()`), not stdout, so colors work even when stdout is captured by the shell widget.
- The selected command is printed to **stdout**, which the shell widget captures via `$(cpt)` and places on the command line.
- The `--install` flag appends a small shell-specific keybinding snippet to the user's shell config file.

## Publishing & Releases

Releases are **fully automated via GitHub Actions**. Do **not** run `goreleaser` locally.

### How to publish a new version:

1. Commit your changes to `main`
2. Tag the commit: `git tag vX.Y.Z`
3. Push with tags: `git push origin main --tags`
4. The GitHub Actions workflow (`.github/workflows/release.yml`) triggers on `v*` tags and runs goreleaser automatically
5. goreleaser builds cross-platform binaries and creates a GitHub Release with assets

### Release workflow details

- **Trigger:** Push of a tag matching `v*`
- **Runner:** `ubuntu-latest`
- **Go version:** Read from `go.mod`
- **goreleaser version:** `~> v2`
- **Config:** `.goreleaser.yml` — builds for darwin/linux/windows × amd64/arm64, CGO disabled, version injected via ldflags
- **Auth:** Uses the automatic `GITHUB_TOKEN` secret — no manual token needed

### Version format

Use semantic versioning: `v0.1.0`, `v0.2.0`, `v1.0.0`, etc. The version string is injected into the binary at build time via `-ldflags "-X main.version={{.Version}}"`.

## Prerequisites for Development

- Go (version in `go.mod`)
- GitHub CLI (`gh`) installed and authenticated (`gh auth login`)
- A GitHub Copilot subscription (needed to test the AI features)
