package main

import (
	"bytes"
	"net"
)

// Telnet protocol bytes (RFC 854).
const (
	telnetIAC  byte = 255 // interpret as command
	telnetSB   byte = 250 // subnegotiation begin
	telnetSE   byte = 240 // subnegotiation end
	telnetWILL byte = 251
	telnetWONT byte = 252
	telnetDO   byte = 253
	telnetDONT byte = 254
)

// Parser states of telnetConn.
const (
	twData = iota
	twIAC
	twOption
	twSubBegin
	twSubData
	twSubIAC
)

// telnetConn is a minimal Telnet transport: it strips IAC sequences from the
// incoming stream and escapes 0xFF in the outgoing data.
//
// It deliberately never answers option negotiation. Reference clients
// (telnet(1), putty in raw mode, expect scripts) do not answer either, and
// some OLTs - FD1304E-B1 among them - drop the session as soon as the client
// starts agreeing to ECHO/SGA/NAWS: the login passes, then the first command
// write returns EOF. Stripping the sequences silently works on every device
// we tested (BDCOM, TP-Link, FD1304E).
type telnetConn struct {
	conn    net.Conn
	pending []byte
	state   int
}

func newTelnetConn(conn net.Conn) *telnetConn {
	return &telnetConn{conn: conn}
}

// Read returns the payload without Telnet commands.
func (t *telnetConn) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		if err := t.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

// Write sends data, doubling 0xFF so it cannot be taken for IAC.
func (t *telnetConn) Write(p []byte) (int, error) {
	if bytes.IndexByte(p, telnetIAC) < 0 {
		return t.conn.Write(p)
	}
	out := make([]byte, 0, len(p)+4)
	for _, b := range p {
		if b == telnetIAC {
			out = append(out, telnetIAC)
		}
		out = append(out, b)
	}
	if _, err := t.conn.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// fill reads one chunk from the socket and appends its payload to pending.
// The state machine survives chunk boundaries: an IAC split between two TCP
// packets is handled correctly.
func (t *telnetConn) fill() error {
	chunk := make([]byte, 4096)
	n, err := t.conn.Read(chunk)
	if n > 0 {
		t.parse(chunk[:n])
	}
	if err != nil && len(t.pending) == 0 {
		return err
	}
	return nil
}

func (t *telnetConn) parse(b []byte) {
	for _, c := range b {
		switch t.state {
		case twData:
			if c == telnetIAC {
				t.state = twIAC
				continue
			}
			t.pending = append(t.pending, c)
		case twIAC:
			switch c {
			case telnetIAC: // escaped 0xFF in the data stream
				t.pending = append(t.pending, c)
				t.state = twData
			case telnetSB:
				t.state = twSubBegin
			case telnetWILL, telnetWONT, telnetDO, telnetDONT:
				t.state = twOption
			default:
				// Single-byte command (NOP, GA, ...) - nothing to do.
				t.state = twData
			}
		case twOption:
			// Option byte of WILL/WONT/DO/DONT, never answered on purpose.
			t.state = twData
		case twSubBegin:
			// Subnegotiation option byte.
			t.state = twSubData
		case twSubData:
			if c == telnetIAC {
				t.state = twSubIAC
			}
		case twSubIAC:
			if c == telnetSE {
				t.state = twData
			} else {
				t.state = twSubData
			}
		}
	}
}
