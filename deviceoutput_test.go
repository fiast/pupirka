package main

import (
	"strings"
	"testing"
)

func TestCheckCommandResultRejectsDeviceErrors(t *testing.T) {
	bad := []string{
		"bad parameter sensitive (line 1 column 22)",
		"syntax error",
		"% Invalid input detected at '^' marker.",
		"Error: unknown command",
		"unknown command",
		"  invalid input  ",
	}
	for _, s := range bad {
		if err := checkCommandResult([]byte(s)); err == nil {
			t.Fatalf("%q must be rejected as a device error", s)
		}
	}
}

func TestCheckCommandResultAcceptsConfigs(t *testing.T) {
	good := []string{
		"",
		"#\nvlan 16\n#\n",
		"hostname r1\n!\nend\n",
		"set enabled=yes\n",
		// a single line that merely mentions an error is a valid config line
		"logging buffered 4096",
	}
	for _, s := range good {
		if err := checkCommandResult([]byte(s)); err != nil {
			t.Fatalf("%q must be accepted, got %v", s, err)
		}
	}
}

func TestCheckCommandResultMessage(t *testing.T) {
	err := checkCommandResult([]byte("bad parameter sensitive (line 1 column 22)"))
	if err == nil || !strings.Contains(err.Error(), "bad parameter") {
		t.Fatalf("unexpected error: %v", err)
	}
}
