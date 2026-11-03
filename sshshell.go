package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSH session modes for a device.
//
// "exec" runs the command over an exec channel (the normal way), "shell" opens
// an interactive CLI with a pty, and "" (auto) tries exec and falls back to the
// shell when the device answers nothing.
const (
	sshModeAuto  = ""
	sshModeExec  = "exec"
	sshModeShell = "shell"
)

// normalizeSSHMode maps the "sshmode" field to a known mode.
func normalizeSSHMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "exec":
		return sshModeExec
	case "shell", "interactive", "cli":
		return sshModeShell
	}
	return sshModeAuto
}

// sshLegacyHostKeyAlgos are the host key algorithms old gear needs. ssh-rsa is
// the SHA-1 variant still used by many switches.
var sshLegacyHostKeyAlgos = []string{
	"ssh-rsa",
	"ssh-dss",
	"rsa-sha2-256",
	"rsa-sha2-512",
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"ssh-ed25519",
}

// sshLegacyMACs keeps hmac-sha1 available for devices that support nothing else.
var sshLegacyMACs = []string{
	"hmac-sha2-256",
	"hmac-sha2-512",
	"hmac-sha1",
	"hmac-sha1-96",
}

// isSSHExitMissing reports whether the device closed the exec channel without
// sending an exit status. Old network devices do that all the time: the output
// is complete and valid, only the exit status is missing.
func isSSHExitMissing(err error) bool {
	var missing *ssh.ExitMissingError
	return errors.As(err, &missing)
}

// SshShellRun drives the device CLI through an interactive shell channel.
//
// Old switches (Eltex MES and relatives) answer nothing over an exec channel
// and print the configuration only in an interactive session with a pty, so
// this path reuses the prompt/pager/cleaning logic of the telnet client.
func SshShellRun(device *Device) ([]byte, error) {
	config, err := SshClientConfig(device)
	if err != nil {
		return nil, err
	}
	address := DeviceAddressFormat(device)
	device.LogDebug(fmt.Sprintf("SshShellRun: dial %s in interactive mode", address))

	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, errors.New("SshShellRun: DialSSH error:" + err.Error())
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, errors.New("SshShellRun: NewSession error:" + err.Error())
	}
	defer session.Close()

	// A pty is required: without it the CLI stays silent on these devices.
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := session.RequestPty("dumb", 1000, 400, modes); err != nil {
		device.LogDebug("SshShellRun: pty request failed: " + err.Error())
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, errors.New("SshShellRun: StdinPipe error:" + err.Error())
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, errors.New("SshShellRun: StdoutPipe error:" + err.Error())
	}
	if err := session.Shell(); err != nil {
		return nil, errors.New("SshShellRun: Shell error:" + err.Error())
	}

	prompt, err := telnetPromptRegexp(device.Prompt)
	if err != nil {
		return nil, fmt.Errorf("SshShellRun: bad prompt %q for device %s: %w", device.Prompt, device.Name, err)
	}

	s := &sshShellSession{
		device: device,
		stdin:  stdin,
		reader: newSSHChunkReader(stdout),
		prompt: prompt,
	}
	return s.run()
}

// sshChunkReader turns a blocking ssh pipe into a channel with timeouts,
// because ssh channels do not support read deadlines.
type sshChunkReader struct {
	ch   chan []byte
	err  error
	done chan struct{}
}

func newSSHChunkReader(r interface{ Read([]byte) (int, error) }) *sshChunkReader {
	c := &sshChunkReader{ch: make(chan []byte, 32), done: make(chan struct{})}
	go func() {
		defer close(c.ch)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case c.ch <- chunk:
				case <-c.done:
					return
				}
			}
			if err != nil {
				c.err = err
				return
			}
		}
	}()
	return c
}

// next returns the next chunk, or ok=false when nothing arrived within d.
func (c *sshChunkReader) next(d time.Duration) (chunk []byte, ok bool, err error) {
	if d <= 0 {
		d = time.Millisecond
	}
	select {
	case b, alive := <-c.ch:
		if !alive {
			if c.err != nil {
				return nil, false, c.err
			}
			return nil, false, nil
		}
		return b, true, nil
	case <-time.After(d):
		return nil, false, nil
	}
}

func (c *sshChunkReader) stop() { close(c.done) }

type sshShellSession struct {
	device       *Device
	stdin        interface{ Write([]byte) (int, error) }
	reader       *sshChunkReader
	prompt       *regexp.Regexp
	pager        int
	pagerDiscard bool
}

func (s *sshShellSession) run() ([]byte, error) {
	defer s.reader.stop()

	if err := s.waitForPrompt("login"); err != nil {
		return nil, err
	}
	for _, cmd := range splitInitCommands(s.device.InitCommand) {
		s.device.LogDebug(fmt.Sprintf("SshShellRun: initcommand %q", cmd))
		if err := s.send(cmd); err != nil {
			return nil, err
		}
		if err := s.waitForPrompt("initcommand " + cmd); err != nil {
			return nil, err
		}
	}

	raw, reason, err := s.execCommand()
	if err != nil {
		return nil, err
	}
	switch reason {
	case telnetStopPrompt:
	case telnetStopIdle:
		s.device.LogWarn(fmt.Sprintf("SshShellRun: prompt not matched for device %s, saving %d bytes as is", s.device.Name, len(raw)))
	case telnetStopDeadline:
		return nil, fmt.Errorf("SshShellRun: commandtimeout %ds exceeded, no prompt from device (read %d bytes)", s.device.CommandTimeout, len(raw))
	case telnetStopEOF:
		return nil, fmt.Errorf("SshShellRun: device closed the shell before the prompt (read %d bytes)", len(raw))
	}

	out := cleanTelnetOutput(raw, s.device.Command, s.prompt)
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.New("SshShellRun: empty output after removing echo and prompt")
	}
	if err := checkCommandResult(out); err != nil {
		return nil, fmt.Errorf("SshShellRun: %w", err)
	}
	return out, nil
}

func (s *sshShellSession) send(line string) error {
	s.device.LogDebug("SshShellRun: >> " + telnetTrace([]byte(line+"\r")))
	_, err := s.stdin.Write([]byte(line + "\r"))
	return err
}

// waitForPrompt waits for the CLI prompt after login or an initcommand.
func (s *sshShellSession) waitForPrompt(stage string) error {
	deadline := time.Now().Add(time.Duration(s.device.Timeout) * time.Second)
	var buf []byte
	for {
		chunk, reason, err := s.read(deadline, 3*time.Second)
		if err != nil {
			return err
		}
		buf = append(buf, chunk...)
		if promptMatchesTail(buf, s.prompt) {
			return nil
		}
		if reason == telnetStopDeadline || reason == telnetStopEOF {
			return fmt.Errorf("SshShellRun: no prompt after %s (%s)", stage, telnetTailString(buf))
		}
		if len(buf) > 1<<20 {
			return errors.New("SshShellRun: device banner too large")
		}
	}
}

func (s *sshShellSession) execCommand() ([]byte, telnetStop, error) {
	timeout := s.device.CommandTimeout
	if timeout <= 0 {
		timeout = s.device.Timeout
	}
	if timeout <= 0 {
		timeout = 10
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	s.device.LogDebug(fmt.Sprintf("SshShellRun: command %q, commandtimeout %ds", s.device.Command, timeout))
	if err := s.send(s.device.Command); err != nil {
		return nil, telnetStopDeadline, err
	}
	return s.readLoop(deadline, true)
}

func (s *sshShellSession) read(deadline time.Time, idle time.Duration) ([]byte, telnetStop, error) {
	return s.readLoopWith(deadline, idle, false)
}

func (s *sshShellSession) readLoop(deadline time.Time, answerPager bool) ([]byte, telnetStop, error) {
	idle := time.Duration(s.device.IdleTimeout) * time.Second
	if idle <= 0 {
		idle = time.Duration(s.device.Timeout) * time.Second
	}
	if idle <= 0 {
		idle = 10 * time.Second
	}
	return s.readLoopWith(deadline, idle, answerPager)
}

func (s *sshShellSession) readLoopWith(deadline time.Time, idle time.Duration, answerPager bool) ([]byte, telnetStop, error) {
	var out []byte
	for {
		if len(out) > telnetMaxOutput {
			return nil, telnetStopDeadline, fmt.Errorf("device output exceeded %d bytes", telnetMaxOutput)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return out, telnetStopDeadline, nil
		}
		wait := idle
		if remaining < wait {
			wait = remaining
		}

		chunk, ok, err := s.reader.next(wait)
		if err != nil {
			if isTelnetClosed(err) {
				return out, telnetStopEOF, nil
			}
			return out, telnetStopDeadline, err
		}
		if ok && len(chunk) > 0 {
			s.device.LogDebug("SshShellRun: << " + telnetTrace(chunk))
			if s.pagerDiscard {
				if rest, found := skipToNextLine(chunk); found {
					chunk = rest
					s.pagerDiscard = false
				} else {
					chunk = nil
				}
			}
			out = append(out, chunk...)
			if answerPager {
				stripped, pages, truncated := stripPagerMarkers(out)
				out = stripped
				if pages > 0 {
					s.pager += pages
					s.pagerDiscard = truncated
					if s.pager > telnetMaxPagerAnswers {
						return out, telnetStopDeadline, errors.New("too many pager prompts, device is not moving")
					}
					if _, werr := s.stdin.Write([]byte(" ")); werr != nil {
						return out, telnetStopDeadline, werr
					}
				}
			}
			if promptMatchesTail(out, s.prompt) {
				return out, telnetStopPrompt, nil
			}
			continue
		}
		if !ok {
			// idle timeout: no more data, treat as the end of the output
			return out, telnetStopIdle, nil
		}
	}
}
