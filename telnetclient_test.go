package main

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.DebugLevel)
	return l
}

func testDevice(name string) Device {
	d := Device{
		Name:           name,
		Address:        "127.0.0.1",
		Username:       "admin",
		Password:       "secret",
		Protocol:       ProtocolTelnet,
		PortTelnet:     23,
		Timeout:        3,
		IdleTimeout:    1,
		CommandTimeout: 5,
		Command:        "show running-config",
	}
	d.Logdevice = testLogger()
	return d
}

// fakeTelnet starts a TCP server on localhost and points device at it.
// A real TCP socket is used instead of net.Pipe because a telnet client writes
// negotiation answers while reading, which would deadlock on a pipe.
func fakeTelnet(t *testing.T, device *Device, script func(c net.Conn)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	device.Address = "127.0.0.1"
	device.Port = uint16(addr.Port)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		script(conn)
	}()
}

// readByteIAC reads one byte, skipping telnet negotiation sent by the client.
func readByteIAC(t *testing.T, c net.Conn) (byte, error) {
	t.Helper()
	var b [1]byte
	for {
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		n, err := c.Read(b[:])
		if err != nil || n == 0 {
			return 0, err
		}
		if b[0] != 0xff { // IAC
			return b[0], nil
		}
		var cmd [1]byte
		if _, err := c.Read(cmd[:]); err != nil {
			return 0, err
		}
		// WILL/WONT/DO/DONT carry an option byte
		if cmd[0] >= 251 && cmd[0] <= 254 {
			var opt [1]byte
			if _, err := c.Read(opt[:]); err != nil {
				return 0, err
			}
		}
	}
}

// readLineIAC reads a CLI line terminated by CR.
func readLineIAC(t *testing.T, c net.Conn) (string, error) {
	t.Helper()
	var b []byte
	for {
		ch, err := readByteIAC(t, c)
		if err != nil {
			return string(b), err
		}
		if ch == '\r' {
			return string(b), nil
		}
		b = append(b, ch)
	}
}

func mustWrite(t *testing.T, c net.Conn, s string) {
	t.Helper()
	_ = c.SetWriteDeadline(time.Now().Add(4 * time.Second))
	if _, err := c.Write([]byte(s)); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

const bdcomBanner = "\xff\xfb\x01\xff\xfb\x03User Access Verification\r\n\r\nUsername: "

// loginScript writes a classic login dialogue and returns after the first prompt.
func loginScript(t *testing.T, c net.Conn, prompt string, user, password string) bool {
	t.Helper()
	mustWrite(t, c, "User Access Verification\r\n\r\nUsername: ")
	if line, err := readLineIAC(t, c); err != nil || line != user {
		t.Errorf("username: got %q, err %v", line, err)
		return false
	}
	mustWrite(t, c, "Password: ")
	if line, err := readLineIAC(t, c); err != nil || line != password {
		t.Errorf("password: got %q, err %v", line, err)
		return false
	}
	mustWrite(t, c, "\r\nWelcome\r\n\r\n"+prompt)
	return true
}

// ---------------------------------------------------------------------------
// prompt handling
// ---------------------------------------------------------------------------

func TestTelnetPromptRegexpGeneric(t *testing.T) {
	re, err := telnetPromptRegexp("")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	cases := []struct {
		name   string
		tail   string
		expect bool
	}{
		{"mikrotik", "export output\n[admin@MikroTik] > ", true},
		{"cisco-user", "Router>", true},
		{"cisco-priv", "Router#", true},
		{"cisco-config", "Router(config)#", true},
		{"prompt-with-trailing-gt", "mes2324#>", false},
		{"eltex", "mes2324#", true},
		{"tplink", "TP-LINK#", true},
		{"bdcom", "\r\nlen2-olt>", true},
		{"config-line", " ip address 10.0.0.1 255.255.255.0", false},
		// TP-Link/Eltex use a lone "#" as a section separator inside
		// show running-config; it must never be taken for a prompt.
		{"tplink-separator", "vlan 16\n#", false},
		{"bare-hash", "#", false},
		{"bare-gt", ">", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := re.MatchString(tc.tail); got != tc.expect {
				t.Fatalf("prompt %q: got %v, want %v", tc.tail, got, tc.expect)
			}
		})
	}
}

func TestPromptAtEndWithLoneCR(t *testing.T) {
	re, err := telnetPromptRegexp("")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	s := &telnetSession{prompt: re}

	// TP-Link ends the prompt line with a lone CR, the next line starts with LF.
	cases := []struct {
		name   string
		tail   string
		expect bool
	}{
		{"crlf", "Welcome\r\n\r\na112>", true},
		{"lf-cr", "Welcome\r\n\r\n\n\ra112>", true},
		{"mikrotik", "\r\n[admin@MikroTik] >", true},
		{"trailing-spaces", "\r\n\r\na112#   ", true},
		{"config-line", "\r\n vlan 16", false},
		{"separator", "\r\n#", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.promptAtEnd([]byte(tc.tail)); got != tc.expect {
				t.Fatalf("promptAtEnd(%q): got %v, want %v", tc.tail, got, tc.expect)
			}
		})
	}
}

func TestTelnetPromptRegexpCustom(t *testing.T) {
	re, err := telnetPromptRegexp(`\[backup@core-1\] >`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !re.MatchString("[backup@core-1] >") {
		t.Fatal("custom prompt did not match")
	}
	if re.MatchString("len2-olt>") {
		t.Fatal("custom prompt must not fall back to generic patterns")
	}
}

func TestTelnetPromptRegexpInvalid(t *testing.T) {
	if _, err := telnetPromptRegexp(`[unclosed`); err == nil {
		t.Fatal("expected a compile error for an invalid prompt")
	}
}

// ---------------------------------------------------------------------------
// output cleaning
// ---------------------------------------------------------------------------

func TestCleanTelnetOutput(t *testing.T) {
	prompt, err := telnetPromptRegexp("")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	cases := []struct {
		name string
		cmd  string
		raw  string
		want string
	}{
		{
			name: "ansi, crlf, echo and prompt",
			cmd:  "show running-config",
			raw:  "show running-config\r\n\x1b[1mbuilding\x1b[0m configuration...\r\nline2\r\nlen2-olt#\r\n",
			want: "building configuration...\nline2\n",
		},
		{
			name: "bdcom caret artifact and double echo",
			cmd:  "show version",
			raw:  "show version\r\nshow version\r\n^\r\nversion 1.0\r\nlen2-olt>",
			want: "version 1.0\n",
		},
		{
			name: "pager leftovers",
			cmd:  "show run",
			raw:  "show run\r\nline1\r\n --More-- \r\nline2\r\n --More-- \r\nlen2-olt#",
			want: "line1\nline2\n",
		},
		{
			name: "backspace erasing",
			cmd:  "show run",
			raw:  "show run\r\nab\x08c\r\nlen2-olt#",
			want: "ac\n",
		},
		{
			name: "only prompt",
			cmd:  "show run",
			raw:  "show run\r\nlen2-olt#",
			want: "",
		},
		{
			name: "no trailing newline on device",
			cmd:  "show running-config",
			raw:  "show running-config\r\nlast line\r\nlen2-olt#",
			want: "last line\n",
		},
		{
			name: "no echo at all",
			cmd:  "show running-config",
			raw:  "hostname r1\r\n!\r\nlen2-olt#",
			want: "hostname r1\n!\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(cleanTelnetOutput([]byte(tc.raw), tc.cmd, prompt))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCleanTelnetOutputEmpty(t *testing.T) {
	if got := cleanTelnetOutput(nil, "cmd", nil); got != nil {
		t.Fatalf("got %q, want nil", got)
	}
	if got := cleanTelnetOutput([]byte("\r\n"), "cmd", nil); got != nil {
		t.Fatalf("got %q, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// full session against a fake device
// ---------------------------------------------------------------------------

func TestTelnetClientRunBDCOMLike(t *testing.T) {
	d := testDevice("len2-olt")
	fakeTelnet(t, &d, func(c net.Conn) {
		mustWrite(t, c, bdcomBanner)

		if line, err := readLineIAC(t, c); err != nil || line != "admin" {
			t.Errorf("username: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "Password: ")
		if line, err := readLineIAC(t, c); err != nil || line != "secret" {
			t.Errorf("password: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "\r\nWelcome to BDCOM\r\n\r\nlen2-olt>")

		if line, err := readLineIAC(t, c); err != nil || line != "show running-config" {
			t.Errorf("command: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "show running-config\r\nhostname len2-olt\r\nline2\r\n --More-- ")

		// the pager marker must be answered with a single space, then the rest follows
		for {
			ch, err := readByteIAC(t, c)
			if err != nil {
				t.Errorf("pager answer: %v", err)
				return
			}
			if ch == ' ' {
				break
			}
		}
		mustWrite(t, c, "\r\nline3\r\nlen2-olt#")
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("TelnetClientRun: %v", err)
	}
	want := "hostname len2-olt\nline2\nline3\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", string(out), want)
	}
}

func TestTelnetClientRunInitCommand(t *testing.T) {
	d := testDevice("cisco")
	d.InitCommand = "terminal length 0"
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "Router>", "admin", "secret") {
			return
		}
		if line, err := readLineIAC(t, c); err != nil || line != "terminal length 0" {
			t.Errorf("initcommand: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "terminal length 0\r\nRouter#")

		if line, err := readLineIAC(t, c); err != nil || line != "show running-config" {
			t.Errorf("command after initcommand: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "show running-config\r\nhostname r1\r\nRouter#")
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("TelnetClientRun: %v", err)
	}
	if string(out) != "hostname r1\n" {
		t.Fatalf("got %q", string(out))
	}
}

func TestTelnetClientRunEnablePassword(t *testing.T) {
	d := testDevice("cisco")
	d.InitCommand = "enable"
	d.EnablePassword = "enablesecret"
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "Router>", "admin", "secret") {
			return
		}
		if line, err := readLineIAC(t, c); err != nil || line != "enable" {
			t.Errorf("enable: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "Password: ")
		if line, err := readLineIAC(t, c); err != nil || line != "enablesecret" {
			t.Errorf("enable password: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "\r\nRouter#")

		if _, err := readLineIAC(t, c); err != nil {
			t.Errorf("command: %v", err)
			return
		}
		mustWrite(t, c, "show running-config\r\nhostname r1\r\nRouter#")
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("TelnetClientRun: %v", err)
	}
	if string(out) != "hostname r1\n" {
		t.Fatalf("got %q", string(out))
	}
}

func TestTelnetClientRunAuthFailure(t *testing.T) {
	d := testDevice("cisco")
	fakeTelnet(t, &d, func(c net.Conn) {
		mustWrite(t, c, "Username: ")
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "Password: ")
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "\r\n% Authentication failed\r\nUsername: ")
	})

	_, err := TelnetClientRun(&d)
	if err == nil {
		t.Fatal("expected an authentication error")
	}
	if !strings.Contains(err.Error(), "rejected authentication") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTelnetClientRunEmptyPassword(t *testing.T) {
	d := testDevice("cisco")
	d.Password = ""
	fakeTelnet(t, &d, func(c net.Conn) {
		mustWrite(t, c, "Username: ")
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "Password: ")
	})

	_, err := TelnetClientRun(&d)
	if err == nil || !strings.Contains(err.Error(), `"password" is empty`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTelnetClientRunIdleKeepsOutput(t *testing.T) {
	d := testDevice("slow")
	d.IdleTimeout = 1
	d.CommandTimeout = 4
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "host>", "admin", "secret") {
			return
		}
		_, _ = readLineIAC(t, c)
		// no prompt ever: the client must fall back to the idle timeout
		mustWrite(t, c, "show running-config\r\nhostname r1\r\n")
		time.Sleep(2500 * time.Millisecond)
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("idle timeout must not fail the backup: %v", err)
	}
	if string(out) != "hostname r1\n" {
		t.Fatalf("got %q", out)
	}
}

func TestTelnetClientRunCommandTimeout(t *testing.T) {
	d := testDevice("stuck")
	d.IdleTimeout = 5
	d.CommandTimeout = 1
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "host>", "admin", "secret") {
			return
		}
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "show running-config\r\nhostname r1\r\n")
		time.Sleep(2500 * time.Millisecond)
	})

	_, err := TelnetClientRun(&d)
	if err == nil || !strings.Contains(err.Error(), "commandtimeout") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTelnetClientRunEmptyOutput(t *testing.T) {
	d := testDevice("empty")
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "host>", "admin", "secret") {
			return
		}
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "show running-config\r\nhost>")
	})

	_, err := TelnetClientRun(&d)
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// address / defaults
// ---------------------------------------------------------------------------

func TestTelnetLoginPromptPatterns(t *testing.T) {
	cases := []struct {
		prompt string
		expect bool
	}{
		{"User:", true},
		{"User: ", true},
		{"User Name:", true},
		{"User Name: ", true},
		{"Username:", true},
		{"Username: ", true},
		{"login:", true},
		{"Login:", true},
		{"Password:", false},
		{"User Access Login", false},
		{"****************************************", false},
	}
	for _, tc := range cases {
		if got := reTelnetLogin.MatchString(tc.prompt); got != tc.expect {
			t.Fatalf("login prompt %q: got %v, want %v", tc.prompt, got, tc.expect)
		}
	}
}

func TestTelnetPagerPatterns(t *testing.T) {
	markers := []string{
		"--More--",
		" --More-- ",
		"<--- More --->",
		"--More or (q)uit--",
		"Press any key to continue (Q to quit)",
		"press any key",
	}
	for _, m := range markers {
		if !reTelnetPager.MatchString(m) {
			t.Fatalf("%q must be treated as a pager marker", m)
		}
		if !reTelnetPagerLine.MatchString(m) {
			t.Fatalf("%q must match a whole pager line", m)
		}
	}
	if reTelnetPagerLine.MatchString(" name \"alt-fed10\"") {
		t.Fatal("a configuration line must not match the pager line pattern")
	}
}

func TestCleanTelnetOutputPagerVariants(t *testing.T) {
	prompt, err := telnetPromptRegexp("")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	raw := "show running-config\r\n!T2600G-28TS-DC\r\n#\r\n" +
		"Press any key to continue (Q to quit)\x00\r\n" +
		"vlan 16\n name \"alt-fed10\"\r\n#\r\na112#"
	want := "!T2600G-28TS-DC\n#\nvlan 16\n name \"alt-fed10\"\n#\n"
	if got := string(cleanTelnetOutput([]byte(raw), "show running-config", prompt)); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTelnetClientRunTPLinkLike(t *testing.T) {
	d := testDevice("a112")
	d.Command = "show running-config"
	d.InitCommand = "enable"
	d.CommandTimeout = 10

	fakeTelnet(t, &d, func(c net.Conn) {
		// exactly what a TP-Link T2600G sends
		mustWrite(t, c, "\xff\xfb\x01\xff\xfb\x03\xff\xfd\x18\xff\xfb\x18"+
			"***************** User Access Login ********************\r\n\r\nUser:")
		if line, err := readLineIAC(t, c); err != nil || line != "admin" {
			t.Errorf("user: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "admin\r\nPassword:")
		if line, err := readLineIAC(t, c); err != nil || line != "secret" {
			t.Errorf("password: got %q, err %v", line, err)
			return
		}
		// the real device mixes "\r\n" and a lone "\r" before the prompt
		mustWrite(t, c, "\r\n\r\n\n\ra112>")

		if line, err := readLineIAC(t, c); err != nil || line != "enable" {
			t.Errorf("enable: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "enable\r\n\n\ra112#")

		if line, err := readLineIAC(t, c); err != nil || line != "show running-config" {
			t.Errorf("command: got %q, err %v", line, err)
			return
		}
		mustWrite(t, c, "show running-config\r\n!T2600G-28TS-DC\r\n#\r\nvlan 16\n name \"alt-fed10\"\r\n#\r\nPress any key to continue")

		for {
			ch, err := readByteIAC(t, c)
			if err != nil {
				t.Errorf("pager answer: %v", err)
				return
			}
			if ch == ' ' {
				break
			}
		}
		// the tail of the marker arrives in a second packet, exactly as the
		// real switch does it
		mustWrite(t, c, " (Q to quit)\x00\r\nvlan 140\r\n name \"pppoe\"\r\n#\r\na112#")
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("TelnetClientRun: %v", err)
	}
	want := "!T2600G-28TS-DC\n#\nvlan 16\n name \"alt-fed10\"\n#\nvlan 140\n name \"pppoe\"\n#\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", string(out), want)
	}
}
func TestSplitInitCommands(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"enable", []string{"enable"}},
		{"enable;terminal length 0", []string{"enable", "terminal length 0"}},
		{"enable;\r\nterminal length 0; ", []string{"enable", "terminal length 0"}},
	}
	for _, tc := range cases {
		got := splitInitCommands(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitInitCommands(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitInitCommands(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		}
	}
}

func TestSshForwardNewDeviceLifecycle(t *testing.T) {
	parent := testDevice("parent")
	parent.Protocol = ProtocolSSH
	parent.Port = 65000
	parent.PortSSH = 65000
	parent.Timeout = 1

	child := testDevice("child")
	child.Address = "10.0.0.1"
	child.Port = 23
	child.PortTelnet = 23

	tunneled, stop, err := SshForwardNewDevice(parent, child)
	if err != nil {
		t.Fatalf("SshForwardNewDevice: %v", err)
	}
	if tunneled.Address != "localhost" {
		t.Fatalf("tunnel address: got %q", tunneled.Address)
	}
	if tunneled.Port == 0 || tunneled.Port == child.Port {
		t.Fatalf("tunnel port: got %d, child port %d", tunneled.Port, child.Port)
	}
	// the child copy handed to the tunnel must keep the real device port
	if got := DeviceAddressFormat(&child); got != "10.0.0.1:23" {
		t.Fatalf("child address: got %q", got)
	}

	stop()

	if _, err := net.DialTimeout("tcp", DeviceAddressFormat(&tunneled), 500*time.Millisecond); err == nil {
		t.Fatal("listener must be closed after stop")
	}
	if _, busy := MLocalPort[tunneled.Port]; busy {
		t.Fatalf("port %d was not released", tunneled.Port)
	}
	stop() // must be idempotent
}

func TestSshNeedForwardMissingParent(t *testing.T) {
	d := testDevice("orphan")
	d.Parent = "does-not-exist"
	if _, _, err := SshNeedForward(&d); err == nil {
		t.Fatal("expected an error for a missing parent device")
	}
}

func TestDeviceAddressFormat(t *testing.T) {
	d := testDevice("a")
	d.Port = 0
	d.PortTelnet = 0
	if got := DeviceAddressFormat(&d); got != "127.0.0.1:23" {
		t.Fatalf("telnet fallback: got %q", got)
	}

	d.Port = 2300
	if got := DeviceAddressFormat(&d); got != "127.0.0.1:2300" {
		t.Fatalf("resolved port: got %q", got)
	}

	ssh := testDevice("b")
	ssh.Protocol = ProtocolSSH
	ssh.Port = 0
	ssh.PortSSH = 2222
	if got := DeviceAddressFormat(&ssh); got != "127.0.0.1:2222" {
		t.Fatalf("ssh: got %q", got)
	}
}

func TestSetDefaultParameterTelnetIgnoresKey(t *testing.T) {
	d := testDevice("t")
	d.Key = "id_rsa"
	d.Timeout = 0
	d.Every = 0
	d.Rotate = 0
	d.TimeFormat = "20060102T1504"
	d.FileNameFormat = "%p%t.rcs"

	SetDefaultParameter(&d)

	if d.Key != "" || d.Authkey {
		t.Fatalf("telnet must ignore key: key=%q authkey=%v", d.Key, d.Authkey)
	}
	if d.Protocol != ProtocolTelnet {
		t.Fatalf("protocol: got %q", d.Protocol)
	}
	if d.Port != d.PortTelnet || d.Port == 0 {
		t.Fatalf("resolved port: got %d, porttelnet %d", d.Port, d.PortTelnet)
	}
	if d.CommandTimeout == 0 || d.IdleTimeout == 0 {
		t.Fatalf("timeouts must default to timeout: idle=%d cmd=%d", d.IdleTimeout, d.CommandTimeout)
	}
}

func TestSetDefaultParameterSSHUnchanged(t *testing.T) {
	d := testDevice("s")
	d.Protocol = ""
	d.Port = 0
	d.PortSSH = 2222
	d.Timeout = 0
	d.Every = 0
	d.Rotate = 0
	d.TimeFormat = "20060102T1504"
	d.FileNameFormat = "%p%t.rcs"

	SetDefaultParameter(&d)

	if d.Protocol != ProtocolSSH {
		t.Fatalf("empty protocol must resolve to ssh, got %q", d.Protocol)
	}
	if d.Port != 2222 {
		t.Fatalf("ssh port must stay portssh, got %d", d.Port)
	}
}

func TestBackupOutputIsStable(t *testing.T) {
	d := testDevice("stable")
	fakeTelnet(t, &d, func(c net.Conn) {
		if !loginScript(t, c, "host>", "admin", "secret") {
			return
		}
		_, _ = readLineIAC(t, c)
		mustWrite(t, c, "show running-config\r\nhostname r1\r\n!\r\nhost>")
	})

	out, err := TelnetClientRun(&d)
	if err != nil {
		t.Fatalf("TelnetClientRun: %v", err)
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Fatalf("output must end with a newline: %q", out)
	}
	if bytes.Contains(out, []byte("host>")) {
		t.Fatalf("prompt leaked into output: %q", out)
	}
}
