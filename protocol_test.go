package main

import (
	"reflect"
	"strings"
	"testing"
)

func runnerPointer(fn backupRunner) uintptr {
	return reflect.ValueOf(fn).Pointer()
}

func TestRunnerFor(t *testing.T) {
	sshPtr := runnerPointer(SshClientRun)
	telnetPtr := runnerPointer(TelnetClientRun)

	cases := []struct {
		name     string
		protocol string
		want     uintptr
	}{
		{"empty is ssh", "", sshPtr},
		{"ssh", "ssh", sshPtr},
		{"ssh upper case", "SSH", sshPtr},
		{"ssh with spaces", " ssh ", sshPtr},
		{"telnet", "telnet", telnetPtr},
		{"telnet upper case", "Telnet", telnetPtr},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Device{Protocol: tc.protocol}
			fn, err := runnerFor(&d)
			if err != nil {
				t.Fatalf("runnerFor(%q): %v", tc.protocol, err)
			}
			if got := runnerPointer(fn); got != tc.want {
				t.Fatalf("runnerFor(%q) returned the wrong backend", tc.protocol)
			}
		})
	}
}

func TestRunnerForUnknown(t *testing.T) {
	d := Device{Protocol: "telnet2"}
	if _, err := runnerFor(&d); err == nil {
		t.Fatal("unknown protocol must be an error, not a silent ssh fallback")
	} else if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeProtocol(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"  SSH ":    "ssh",
		"Telnet":    "telnet",
		"TELNET\t":  "telnet",
		" telnet  ": "telnet",
	}
	for in, want := range cases {
		if got := normalizeProtocol(in); got != want {
			t.Fatalf("normalizeProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllowedProtocol(t *testing.T) {
	allowed := []string{"", "ssh", "telnet"}
	for _, p := range allowed {
		if !allowedProtocol(p) {
			t.Fatalf("%q must be allowed", p)
		}
	}
	for _, p := range []string{"telnet2", "ssl", "sftp"} {
		if allowedProtocol(p) {
			t.Fatalf("%q must not be allowed", p)
		}
	}
}
