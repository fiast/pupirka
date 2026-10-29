package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ziutek/telnet"
)

const (
	// telnetDefaultPort is used when neither the device nor the config sets porttelnet.
	telnetDefaultPort = 23
	// telnetMaxOutput protects against a runaway command filling the memory.
	telnetMaxOutput = 32 << 20 // 32 MiB
	// telnetTailSize is how much of the stream is inspected when looking for a prompt.
	telnetTailSize = 8 << 10
	// telnetMaxPagerAnswers caps the automatic "--More--" handling.
	telnetMaxPagerAnswers = 500
	// telnetMaxLoginSteps caps prompt/answer round trips during login.
	telnetMaxLoginSteps = 6
)

// telnetPromptPatterns are generic CLI prompts. They are used when the device
// JSON has no "prompt" value, so a device file can stay minimal.
var telnetPromptPatterns = []string{
	// MikroTik: [admin@MikroTik] >
	`(?m)^\[[^\]\r\n]*@[^\]\r\n]*\]\s*>\s*$`,
	// Cisco/Eltex/TP-Link/BDCOM: host> host# host(config)#
	// A bare ">" or "#" must NOT match: TP-Link and Eltex print "#" as a
	// section separator inside show running-config, and treating it as a
	// prompt cuts the backup at the first separator.
	`(?m)^[\w.\-@:/()]+[>#]\s*$`,
}

var (
	// "Login:", "User:", "User Name:", "Username:" (TP-Link uses a bare "User:").
	reTelnetLogin    = regexp.MustCompile(`(?i)(login|user(\s*name)?)\s*[:>]\s*$`)
	reTelnetPassword = regexp.MustCompile(`(?i)(password|passwd)\s*[:>]\s*$`)
	reTelnetAuthFail = regexp.MustCompile(`(?i)((invalid|incorrect|wrong)\s+(user|user\s*name|name|password|login))|authentication\s+fail|access\s+denied|too\s+many\s+(failed|login)`)
	// Pager markers of the vendors we meet: Cisco "--More--", MikroTik "--More--",
	// Huawei/Eltex "<--- More --->", TP-Link "Press any key to continue (Q to quit)".
	// The "press any key" form is taken up to the end of the line: the rest of
	// the sentence must not leak into the backup.
	reTelnetPager = regexp.MustCompile(`(?i)(--+\s*more\s*--+|<---?\s*more\s*--->?|--+\s*more\s+or\s+\(q\)uit\s*--+|press\s+any\s+key[^\r\n]*)`)
	// A pager marker that occupies a whole line, with the blanks around it.
	reTelnetPagerLine = regexp.MustCompile(`(?i)^\s*(--+\s*more\s*--+|<---?\s*more\s*--->?|--+\s*more\s+or\s+\(q\)uit\s*--+|press\s+any\s+key[^\r\n]*)\s*$`)
	// ANSI escape sequences (colour, cursor movement) some CLIs send.
	reTelnetANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>]`)
	// "char\b" pairs used by CLIs to erase a character.
	reTelnetBackspace = regexp.MustCompile(`[^\n]\x08`)
	// Remaining control characters that are never part of a configuration.
	reTelnetCtrlChars = regexp.MustCompile(`[\x00\x07\x0b\x0c\x0e-\x1f]`)
)

// telnetStop says why the read loop finished.
type telnetStop int

const (
	telnetStopPrompt telnetStop = iota
	telnetStopIdle
	telnetStopDeadline
	telnetStopEOF
)

func (r telnetStop) String() string {
	switch r {
	case telnetStopPrompt:
		return "prompt matched"
	case telnetStopIdle:
		return "idle timeout"
	case telnetStopDeadline:
		return "command timeout"
	case telnetStopEOF:
		return "connection closed"
	}
	return "unknown"
}

type telnetReadOptions struct {
	deadline    time.Time
	idle        time.Duration
	stop        func([]byte) bool
	answerPager bool
}

type telnetSession struct {
	device       *Device
	conn         *telnet.Conn
	raw          net.Conn
	prompt       *regexp.Regexp
	pager        int
	pagerDiscard bool // swallow the rest of the pager line (it was split by TCP)
}

// TelnetClientRun connects to the device over telnet, runs device.Command and
// returns the configuration without the login dialogue, the command echo and
// the trailing prompt, so it can be compared byte by byte with the previous
// backup (see SaveBackupFile / FileCompareByteBool).
func TelnetClientRun(device *Device) ([]byte, error) {
	target := DeviceAddressFormat(device)
	timeout := time.Duration(device.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	device.LogDebug(fmt.Sprintf("TelnetClientRun: dial %s, protocol telnet", target))

	dialer := net.Dialer{Timeout: timeout}
	raw, err := dialer.Dial("tcp", target)
	if err != nil {
		return nil, errors.New("TelnetClientRun: dial " + target + ": " + err.Error())
	}
	defer raw.Close()
	device.LogDebug(fmt.Sprintf("TelnetClientRun: connected to %s", target))

	return telnetSessionRun(device, raw)
}

// telnetSessionRun wraps an established connection and runs the backup.
// It is split out from TelnetClientRun so tests can drive it over net.Pipe.
func telnetSessionRun(device *Device, raw net.Conn) ([]byte, error) {
	conn, err := telnet.NewConn(raw)
	if err != nil {
		return nil, errors.New("TelnetClientRun: telnet negotiation: " + err.Error())
	}
	session, err := newTelnetSession(device, conn, raw)
	if err != nil {
		return nil, err
	}
	return session.Run()
}

func newTelnetSession(device *Device, conn *telnet.Conn, raw net.Conn) (*telnetSession, error) {
	prompt, err := telnetPromptRegexp(device.Prompt)
	if err != nil {
		return nil, fmt.Errorf("TelnetClientRun: bad prompt %q for device %s: %w", device.Prompt, device.Name, err)
	}
	return &telnetSession{device: device, conn: conn, raw: raw, prompt: prompt}, nil
}

// telnetPromptRegexp compiles the device prompt. A custom value is anchored to
// a whole line; without a custom value the generic patterns are used.
func telnetPromptRegexp(custom string) (*regexp.Regexp, error) {
	if strings.TrimSpace(custom) != "" {
		return regexp.Compile(`(?m)^(?:` + custom + `)\s*$`)
	}
	return regexp.Compile(strings.Join(telnetPromptPatterns, "|"))
}

func (s *telnetSession) Run() ([]byte, error) {
	if err := s.authenticate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.device.InitCommand) != "" {
		for _, cmd := range splitInitCommands(s.device.InitCommand) {
			if err := s.runInitCommand(cmd); err != nil {
				return nil, err
			}
		}
	}

	raw, reason, err := s.execCommand()
	if err != nil {
		return nil, err
	}
	switch reason {
	case telnetStopPrompt:
		// normal case
	case telnetStopIdle:
		// Keep the data: a prompt pattern that does not match the device must
		// not destroy the backup, but it has to be visible in the log.
		s.device.LogWarn(fmt.Sprintf("TelnetClientRun: prompt not matched for device %s, saving %d bytes as is (set \"prompt\" in the device JSON to fix)", s.device.Name, len(raw)))
	case telnetStopDeadline:
		return nil, fmt.Errorf("TelnetClientRun: commandtimeout %ds exceeded, no prompt from device (read %d bytes)", s.device.CommandTimeout, len(raw))
	case telnetStopEOF:
		return nil, fmt.Errorf("TelnetClientRun: device closed the connection before the prompt (read %d bytes)", len(raw))
	}

	out := cleanTelnetOutput(raw, s.device.Command, s.prompt)
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.New("TelnetClientRun: empty output after removing echo and prompt")
	}
	s.device.LogDebug(fmt.Sprintf("TelnetClientRun: got %d bytes for device %s", len(out), s.device.Name))
	return out, nil
}

// authenticate walks the login dialogue: login/user prompt, password prompt, prompt.
func (s *telnetSession) authenticate() error {
	deadline := time.Now().Add(time.Duration(s.device.Timeout) * time.Second)
	var buf []byte
	for step := 0; step < telnetMaxLoginSteps; step++ {
		chunk, reason, err := s.readStep(deadline)
		if err != nil {
			return err
		}
		buf = append(buf, chunk...)
		if len(buf) > 1<<20 {
			return errors.New("TelnetClientRun: login output too large")
		}
		if reTelnetAuthFail.Match(trimPromptTail(buf)) {
			return fmt.Errorf("TelnetClientRun: device rejected authentication: %s", telnetTailString(buf))
		}
		switch {
		case reTelnetPassword.Match(trimPromptTail(buf)):
			if s.device.Password == "" {
				return errors.New(`TelnetClientRun: device asks for a password, but "password" is empty`)
			}
			s.device.LogDebug("TelnetClientRun: sending password")
			if err := s.writeLine(s.device.Password); err != nil {
				return err
			}
			buf = buf[:0]
		case reTelnetLogin.Match(trimPromptTail(buf)):
			if s.device.Username == "" {
				return errors.New(`TelnetClientRun: device asks for a login, but "username" is empty`)
			}
			s.device.LogDebug("TelnetClientRun: sending username")
			if err := s.writeLine(s.device.Username); err != nil {
				return err
			}
			buf = buf[:0]
		case s.promptAtEnd(buf):
			s.device.LogDebug("TelnetClientRun: logged in")
			return nil
		case reason == telnetStopIdle:
			return fmt.Errorf("TelnetClientRun: no login prompt from device (%s)", telnetTailString(buf))
		case reason == telnetStopEOF:
			return errors.New("TelnetClientRun: device closed the connection during login")
		case reason == telnetStopDeadline:
			return errors.New("TelnetClientRun: timeout during login")
		}
	}
	return errors.New("TelnetClientRun: login dialogue did not converge")
}

// splitInitCommands splits initcommand on ";" and newlines, so a device can
// need more than one preparatory command ("enable;terminal length 0").
func splitInitCommands(init string) []string {
	fields := strings.FieldsFunc(init, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r'
	})
	cmds := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			cmds = append(cmds, f)
		}
	}
	return cmds
}

// runInitCommand executes one preparatory command (for example "enable" on
// Cisco, "terminal length 0" on Cisco/Eltex/BDCOM) and waits for the prompt.
func (s *telnetSession) runInitCommand(cmd string) error {
	deadline := time.Now().Add(time.Duration(s.device.CommandTimeout) * time.Second)
	s.device.LogDebug(fmt.Sprintf("TelnetClientRun: initcommand %q", cmd))
	if err := s.writeLine(cmd); err != nil {
		return err
	}
	var buf []byte
	for step := 0; step < 4; step++ {
		chunk, reason, err := s.readStep(deadline)
		if err != nil {
			return err
		}
		buf = append(buf, chunk...)
		switch {
		case reTelnetPassword.Match(trimPromptTail(buf)):
			if s.device.EnablePassword == "" {
				return fmt.Errorf("TelnetClientRun: device asks for an enable password after initcommand %q, set \"enablepassword\"", cmd)
			}
			s.device.LogDebug("TelnetClientRun: sending enable password")
			if err := s.writeLine(s.device.EnablePassword); err != nil {
				return err
			}
			buf = buf[:0]
		case reTelnetAuthFail.Match(trimPromptTail(buf)):
			return fmt.Errorf("TelnetClientRun: initcommand %q rejected: %s", cmd, telnetTailString(buf))
		case s.promptAtEnd(buf):
			return nil
		case reason == telnetStopIdle || reason == telnetStopDeadline:
			return fmt.Errorf("TelnetClientRun: no prompt after initcommand %q (%s)", cmd, telnetTailString(buf))
		case reason == telnetStopEOF:
			return errors.New("TelnetClientRun: device closed the connection after initcommand")
		}
	}
	return fmt.Errorf("TelnetClientRun: initcommand %q did not settle", cmd)
}

// execCommand sends the backup command and reads until the prompt.
func (s *telnetSession) execCommand() ([]byte, telnetStop, error) {
	timeout := s.device.CommandTimeout
	if timeout <= 0 {
		timeout = s.device.Timeout
	}
	if timeout <= 0 {
		timeout = 10
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	s.device.LogDebug(fmt.Sprintf("TelnetClientRun: command %q, commandtimeout %ds", s.device.Command, timeout))
	if err := s.writeLine(s.device.Command); err != nil {
		return nil, telnetStopDeadline, err
	}
	return s.readLoop(telnetReadOptions{
		deadline:    deadline,
		idle:        s.idleTimeout(),
		stop:        s.promptAtEnd,
		answerPager: true,
	})
}

// readStep reads until something needs an answer (login, password, prompt, error).
func (s *telnetSession) readStep(deadline time.Time) ([]byte, telnetStop, error) {
	return s.readLoop(telnetReadOptions{
		deadline: deadline,
		idle:     s.idleTimeout(),
		stop: func(b []byte) bool {
			t := trimPromptTail(b)
			return reTelnetLogin.Match(t) ||
				reTelnetPassword.Match(t) ||
				reTelnetAuthFail.Match(t) ||
				s.promptAtEnd(b)
		},
		answerPager: true,
	})
}

func (s *telnetSession) idleTimeout() time.Duration {
	idle := time.Duration(s.device.IdleTimeout) * time.Second
	if idle <= 0 {
		idle = time.Duration(s.device.Timeout) * time.Second
	}
	if idle <= 0 {
		idle = 10 * time.Second
	}
	return idle
}

func (s *telnetSession) readLoop(opt telnetReadOptions) ([]byte, telnetStop, error) {
	var out []byte
	buf := make([]byte, 4096)
	idleRetries := 0
	for {
		if len(out) > telnetMaxOutput {
			return nil, telnetStopDeadline, fmt.Errorf("device output exceeded %d bytes", telnetMaxOutput)
		}
		if !time.Now().Before(opt.deadline) {
			return out, telnetStopDeadline, nil
		}
		readDeadline := time.Now().Add(opt.idle)
		if readDeadline.After(opt.deadline) {
			readDeadline = opt.deadline
		}
		if err := s.raw.SetReadDeadline(readDeadline); err != nil {
			return out, telnetStopDeadline, err
		}

		n, err := s.conn.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// The pager marker may be split between packets ("Press any key to
			// continue" / " (Q to quit)"): drop the rest of that line so the
			// tail of the marker cannot leak into the backup.
			if s.pagerDiscard {
				if rest, ok := skipToNextLine(chunk); ok {
					chunk = rest
					s.pagerDiscard = false
				} else {
					chunk = nil
				}
			}
			out = append(out, chunk...)
			if opt.answerPager {
				var truncated bool
				var pages int
				out, pages, truncated = s.stripPager(out)
				if pages > 0 {
					s.pagerDiscard = truncated
					if s.pager > telnetMaxPagerAnswers {
						return out, telnetStopDeadline, errors.New("too many pager prompts, device is not moving")
					}
					if werr := s.writeRaw(" "); werr != nil {
						return out, telnetStopDeadline, werr
					}
				}
			}
			if opt.stop(out) {
				return out, telnetStopPrompt, nil
			}
		}

		if err != nil {
			if isTelnetTimeout(err) {
				// A pager marker split between two packets leaves the device
				// waiting for a key press: answer once more before giving up.
				if opt.answerPager && idleRetries < 3 && s.looksLikePagerTail(out) {
					idleRetries++
					out, _, s.pagerDiscard = s.stripPager(out)
					if werr := s.writeRaw(" "); werr != nil {
						return out, telnetStopDeadline, werr
					}
					continue
				}
				if !time.Now().Before(opt.deadline) {
					return out, telnetStopDeadline, nil
				}
				return out, telnetStopIdle, nil
			}
			if isTelnetClosed(err) {
				return out, telnetStopEOF, nil
			}
			return out, telnetStopDeadline, err
		}
		if n == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// writeLine sends one CLI line. CR only: "line\r\n" makes some CLIs echo a
// stray "^" and treat the LF as a second, empty command.
func (s *telnetSession) writeLine(line string) error {
	return s.writeRaw(line + "\r")
}

func (s *telnetSession) writeRaw(data string) error {
	_, err := s.conn.Write([]byte(data))
	return err
}

// promptAtEnd reports whether the inspected tail ends with the device prompt.
func (s *telnetSession) promptAtEnd(b []byte) bool {
	if s.prompt == nil {
		return false
	}
	t := trimPromptTail(b)
	if len(t) == 0 {
		return false
	}
	return s.prompt.Match(t)
}

// trimPromptTail returns the last telnetTailSize bytes without trailing blanks,
// so anchored prompt patterns match the end of the stream.
//
// CR is normalised to LF first: some CLIs (TP-Link) end the prompt line with a
// lone "\r" ("\n\ra112>"), and the multiline "^" of Go only anchors after "\n".
func trimPromptTail(b []byte) []byte {
	t := b
	if len(t) > telnetTailSize {
		t = t[len(t)-telnetTailSize:]
	}
	if bytes.IndexByte(t, '\r') >= 0 {
		t = bytes.ReplaceAll(t, []byte("\r"), []byte("\n"))
	}
	return bytes.TrimRight(t, "\n\t \x00")
}

// stripPager removes pager markers from buf and returns how many were removed.
func (s *telnetSession) stripPager(buf []byte) ([]byte, int, bool) {
	idx := reTelnetPager.FindAllIndex(buf, -1)
	if len(idx) == 0 {
		return buf, 0, false
	}
	s.pager += len(idx)
	out := make([]byte, 0, len(buf))
	prev := 0
	truncated := false
	for _, m := range idx {
		out = append(out, buf[prev:m[0]]...)
		prev = m[1]
		// The marker is printed on its own line: take its line ending with it.
		for prev < len(buf) && (buf[prev] == '\r' || buf[prev] == '\n') {
			prev++
		}
		if prev == len(buf) {
			// The rest of the marker line may arrive in the next packet.
			truncated = true
		}
	}
	out = append(out, buf[prev:]...)
	return out, len(idx), truncated
}

// skipToNextLine drops everything up to and including the next line ending.
// It reports false when the buffer has no line ending yet.
func skipToNextLine(b []byte) ([]byte, bool) {
	for i := 0; i < len(b); i++ {
		if b[i] != '\r' && b[i] != '\n' {
			continue
		}
		j := i
		for j < len(b) && (b[j] == '\r' || b[j] == '\n') {
			j++
		}
		return b[j:], true
	}
	return nil, false
}

// looksLikePagerTail is a safety net for a marker split between two reads.
func (s *telnetSession) looksLikePagerTail(b []byte) bool {
	t := strings.ToLower(string(trimPromptTail(b)))
	if t == "" {
		return false
	}
	for _, suffix := range []string{"mo", "mor", "more", "-", "--", "---", "<", "<-", "<--"} {
		if strings.HasSuffix(t, suffix) {
			return true
		}
	}
	return false
}

func isTelnetTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, os.ErrDeadlineExceeded)
}

func isTelnetClosed(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed)
}

// cleanTelnetOutput turns the raw stream of one command into the backup body:
// no ANSI escapes, no command echo, no pager markers, no trailing prompt.
func cleanTelnetOutput(raw []byte, command string, prompt *regexp.Regexp) []byte {
	if len(raw) == 0 {
		return nil
	}

	b := reTelnetANSI.ReplaceAll(raw, nil)
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte{0}, nil)

	for i := 0; i < 3; i++ {
		next := reTelnetBackspace.ReplaceAll(b, nil)
		if bytes.Equal(next, b) {
			break
		}
		b = next
	}
	b = reTelnetCtrlChars.ReplaceAll(b, nil)

	lines := strings.Split(string(b), "\n")

	// Drop pager markers. A marker on its own line takes the whole line with
	// it (and the blanks the CLI padded it with), otherwise it is cut out of
	// the line it shares with real output.
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if reTelnetPagerLine.MatchString(line) {
			continue
		}
		// A marker already cut out by the read loop leaves the blanks that
		// surrounded it; a configuration line of blanks carries no meaning.
		if line != "" && strings.TrimSpace(line) == "" {
			continue
		}
		if reTelnetPager.MatchString(line) {
			line = reTelnetPager.ReplaceAllString(line, "")
			if strings.TrimSpace(line) == "" {
				continue
			}
		}
		filtered = append(filtered, line)
	}
	lines = filtered

	// Drop the echoed command and the leading noise that came with it.
	cmd := strings.ToLower(strings.TrimSpace(command))
	drop := 0
	for drop < len(lines) && drop < 4 {
		line := strings.TrimSpace(lines[drop])
		if line == "" || line == "^" {
			drop++
			continue
		}
		if len(cmd) >= 3 && strings.Contains(strings.ToLower(line), cmd) {
			drop++
			continue
		}
		break
	}
	lines = lines[drop:]

	// Drop the trailing prompt and blank lines.
	for len(lines) > 0 {
		line := strings.TrimSpace(lines[len(lines)-1])
		if line == "" || line == "^" || (prompt != nil && prompt.MatchString(line)) {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}

	out := strings.TrimRight(strings.Join(lines, "\n"), " \t\n")
	if out == "" {
		return nil
	}
	return []byte(out + "\n")
}

// telnetTailString renders the tail of a buffer for error messages.
func telnetTailString(b []byte) string {
	const max = 200
	t := trimPromptTail(b)
	if len(t) > max {
		t = t[len(t)-max:]
	}
	q := strconv.Quote(string(t))
	if len(q) > max+32 {
		q = q[:max+32] + "..."
	}
	return q
}
