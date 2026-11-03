package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeSSHMode(t *testing.T) {
	cases := map[string]string{
		"":             sshModeAuto,
		"  ":           sshModeAuto,
		"exec":         sshModeExec,
		"EXEC ":        sshModeExec,
		"shell":        sshModeShell,
		"Shell":        sshModeShell,
		" interactive": sshModeShell,
		"cli":          sshModeShell,
		"whatever":     sshModeAuto,
	}
	for in, want := range cases {
		if got := normalizeSSHMode(in); got != want {
			t.Fatalf("normalizeSSHMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEltexPagerPattern(t *testing.T) {
	marker := "More: <space>,  Quit: q or CTRL+Z, One line: <return> "
	if !reTelnetPager.MatchString(marker) {
		t.Fatalf("%q must be treated as a pager marker", marker)
	}
	if !reTelnetPagerLine.MatchString(marker) {
		t.Fatalf("%q must match a whole pager line", marker)
	}
	if reTelnetPagerLine.MatchString(" interface gigabitethernet 0/1") {
		t.Fatal("a configuration line must not match the pager pattern")
	}
}

func TestCleanTelnetOutputEltexPager(t *testing.T) {
	prompt, err := telnetPromptRegexp("")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	raw := "show running-config\r\nhostname core01\r\n!\r\n" +
		"\x1b[0mMore: <space>,  Quit: q or CTRL+Z, One line: <return> \r\n" +
		"interface gigabitethernet 0/1\r\ncore01#"
	want := "hostname core01\n!\ninterface gigabitethernet 0/1\n"
	got := string(cleanTelnetOutput([]byte(raw), "show running-config", prompt))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSSHDialHint(t *testing.T) {
	authErr := fmt.Errorf("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	if got := sshDialHint(authErr); !strings.Contains(got, "rejected the credentials") {
		t.Fatalf("auth error must carry a hint, got %q", got)
	}

	dsaErr := fmt.Errorf("ssh: handshake failed: ssh: unsupported DSA key size 512")
	if got := sshDialHint(dsaErr); !strings.Contains(got, "telnet") {
		t.Fatalf("DSA error must suggest telnet, got %q", got)
	}

	plain := fmt.Errorf("ssh: handshake failed: ssh: no common algorithm for key exchange")
	if got := sshDialHint(plain); !strings.Contains(got, "deprecated crypto") {
		t.Fatalf("algorithm error must carry a hint, got %q", got)
	}

	if got := sshDialHint(fmt.Errorf("i/o timeout")); got != "i/o timeout" {
		t.Fatalf("unknown error must pass through, got %q", got)
	}
}

func TestSSHModeIgnoredForTelnet(t *testing.T) {
	d := testDevice("telnet-with-sshmode")
	d.Protocol = ProtocolTelnet
	d.SSHMode = "shell"
	d.Timeout = 0
	d.Every = 0
	d.Rotate = 0
	d.TimeFormat = "20060102T1504"
	d.FileNameFormat = "%p%t.rcs"

	SetDefaultParameter(&d)

	if d.SSHMode != sshModeAuto {
		t.Fatalf("sshmode must be reset for a telnet device, got %q", d.SSHMode)
	}
}

func TestSSHModeFromDefault(t *testing.T) {
	ConfigV.Set("devicedefault.sshmode", "shell")
	defer ConfigV.Set("devicedefault.sshmode", "")

	d := testDevice("ssh-default-mode")
	d.Protocol = ProtocolSSH
	d.SSHMode = ""
	d.Timeout = 0
	d.Every = 0
	d.Rotate = 0
	d.TimeFormat = "20060102T1504"
	d.FileNameFormat = "%p%t.rcs"

	SetDefaultParameter(&d)

	if d.SSHMode != sshModeShell {
		t.Fatalf("sshmode must come from devicedefault, got %q", d.SSHMode)
	}
}
