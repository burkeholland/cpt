# cpt

Add GitHub Copilot chat to any terminal. Press **Ctrl+K**, describe what you want in plain English, and get a shell command back — ready to run.

Works with **zsh**, **bash**, **fish**, and **PowerShell** on macOS, Linux, and Windows.

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/burkeholland/cpt/main/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/burkeholland/cpt/main/install.ps1 | iex
```

The installer downloads the binary, adds it to your PATH, and registers the **Ctrl+K** keybinding in your shell. Restart your terminal to activate.

### Alternative: install with Go

```sh
go install github.com/burkeholland/cpt@latest
cpt --install
```

## Usage

1. Press **Ctrl+K** anywhere in your terminal
2. Type what you want (e.g. "kill process on port 3000")
3. Press **Enter** to accept the command, or type to refine
4. Press **Esc** to cancel

Use **Tab** / **Shift+Tab** to switch between Copilot models. Your model choice is remembered across sessions.

You can also run `cpt` directly:

```sh
cpt "find all node_modules and delete them"
```

## Prerequisites

- [GitHub Copilot](https://github.com/features/copilot) subscription
- [GitHub CLI](https://cli.github.com/) installed and authenticated (`gh auth login`)

## How it works

cpt uses the [Copilot SDK](https://github.com/github/copilot-sdk) to stream responses from Copilot. It renders a TUI overlay on the terminal's alternate screen — the UI appears on Ctrl+K and vanishes completely when you're done.

The selected command is printed to stdout, which the shell widget captures and places on your command line ready to execute.

## Shell widget details

`cpt --install` appends a small snippet to your shell config:

| Shell      | Config file                          |
|------------|--------------------------------------|
| zsh        | `~/.zshrc`                           |
| bash       | `~/.bashrc`                          |
| fish       | `~/.config/fish/config.fish`         |
| PowerShell | `$PROFILE` (resolved dynamically)    |

To see the snippets without installing: `cpt --setup`

## Uninstall

1. Remove the `# cpt - terminal copilot` block from your shell config
2. Delete the binary (`which cpt` to find it)

## License

MIT

---

Built by [Burke Holland](https://burkeholland.github.io)
