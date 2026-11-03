package main

import (
	"fmt"
	"regexp"
	"strings"
)

// reDeviceError matches single-line CLI error replies. Saving such a reply as a
// configuration would silently replace a good backup with an error message, so
// the backup fails instead (and the error hook fires).
var reDeviceError = regexp.MustCompile(`(?i)^\s*(bad parameter|syntax error|invalid input|invalid command|unknown command|unrecognized command|error[: ]|no such command|%\s*invalid|%\s*incomplete)`)

// checkCommandResult rejects an output that is obviously a CLI error message.
//
// Only a single-line output is inspected: a real configuration never consists
// of one line that starts with "bad parameter" and friends.
func checkCommandResult(out []byte) error {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	if strings.Contains(trimmed, "\n") {
		return nil
	}
	if reDeviceError.MatchString(trimmed) {
		return fmt.Errorf("device returned an error instead of a configuration: %q", trimmed)
	}
	return nil
}
