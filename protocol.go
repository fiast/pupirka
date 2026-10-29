package main

import (
	"fmt"
	"strings"
)

// Supported device protocols. An empty protocol means "ssh" so that device
// files written before the "protocol" field existed keep working unchanged.
const (
	ProtocolSSH    = "ssh"
	ProtocolTelnet = "telnet"
)

// backupRunner fetches the device configuration.
//
// Both protocol implementations have exactly this signature, which keeps the
// dispatch in backup() a single call and lets SSH stay untouched.
type backupRunner func(device *Device) ([]byte, error)

// normalizeProtocol lowercases and trims a protocol value.
func normalizeProtocol(protocol string) string {
	return strings.ToLower(strings.TrimSpace(protocol))
}

// allowedProtocol reports whether the protocol (after normalisation) is known.
func allowedProtocol(protocol string) bool {
	switch protocol {
	case "", ProtocolSSH, ProtocolTelnet:
		return true
	}
	return false
}

// runnerFor returns the backup implementation for the device protocol.
//
// Unknown protocols are an error on purpose: silently falling back to SSH
// would turn a typo in the device JSON into a confusing login failure.
func runnerFor(device *Device) (backupRunner, error) {
	switch normalizeProtocol(device.Protocol) {
	case "", ProtocolSSH:
		return SshClientRun, nil
	case ProtocolTelnet:
		return TelnetClientRun, nil
	}
	return nil, fmt.Errorf("protocol %q not supported, use %q or %q", device.Protocol, ProtocolSSH, ProtocolTelnet)
}
