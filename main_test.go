package main

import (
	"strings"
	"testing"
)

func TestRemoveInstalledWidgetExplicitBlock(t *testing.T) {
	content := strings.Join([]string{
		"before",
		widgetStartMarker,
		"# cpt - terminal copilot (Ctrl+K)",
		"cpt-widget() {",
		"}",
		widgetEndMarker,
		"after",
	}, "\n")

	got, err := removeInstalledWidget(content)
	if err != nil {
		t.Fatal(err)
	}
	if got != "before\nafter" {
		t.Fatalf("unexpected cleaned content: %q", got)
	}
}

func TestRemoveInstalledWidgetLegacyPowerShellBlock(t *testing.T) {
	content := strings.Join([]string{
		"$env:BEFORE = 'yes'",
		"# cpt - terminal copilot (Ctrl+K)",
		"if (Get-Command Set-PSReadLineKeyHandler -ErrorAction SilentlyContinue) {",
		"    function Invoke-Cpt {",
		"    }",
		"    Set-PSReadLineKeyHandler -Chord 'Ctrl+k' -ScriptBlock { Invoke-Cpt }",
		"}",
		"$env:AFTER = 'yes'",
	}, "\n")

	got, err := removeInstalledWidget(content)
	if err != nil {
		t.Fatal(err)
	}
	if got != "$env:BEFORE = 'yes'\n$env:AFTER = 'yes'" {
		t.Fatalf("unexpected cleaned content: %q", got)
	}
}

func TestRemoveInstalledWidgetRejectsIncompleteBlock(t *testing.T) {
	_, err := removeInstalledWidget(widgetStartMarker + "\nfunction cpt-widget")
	if err == nil {
		t.Fatal("expected incomplete widget block to fail")
	}
}

func TestShellEscape(t *testing.T) {
	if got := shellEscape("/tmp/it's/cpt", "zsh"); got != `'/tmp/it'\''s/cpt'` {
		t.Fatalf("unexpected POSIX escaping: %q", got)
	}
	if got := shellEscape(`C:\It's\cpt.exe`, "powershell"); got != `'C:\It''s\cpt.exe'` {
		t.Fatalf("unexpected PowerShell escaping: %q", got)
	}
}
