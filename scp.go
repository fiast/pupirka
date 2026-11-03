package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scpPeer is the wire the scp sink protocol runs over: acks arrive from the
// remote sink and commands/file data go out to it.
type scpPeer interface {
	io.Reader
	io.Writer
}

// scpSender uploads a directory tree using the scp "sink" protocol (scp -t).
//
// It is implemented here instead of pulling an sftp module: the protocol is
// tiny, needs no extra dependency, and the remote side only has to provide the
// scp binary, which is present on every NAS and every unix host.
type scpSender struct {
	peer  scpPeer
	br    *bufio.Reader
	files int
	bytes int64
}

func newSCPSender(peer scpPeer) *scpSender {
	return &scpSender{peer: peer, br: bufio.NewReader(peer)}
}

// readAck reads the sink reply: a zero byte means "ok", 1 or 2 is an error
// followed by a human readable line.
func (s *scpSender) readAck() error {
	b, err := s.br.ReadByte()
	if err != nil {
		return fmt.Errorf("scp: read ack: %w", err)
	}
	switch b {
	case 0:
		return nil
	case 1, 2:
		msg, _ := s.br.ReadString('\n')
		return fmt.Errorf("scp: remote error: %s", strings.TrimSpace(msg))
	}
	return fmt.Errorf("scp: unexpected reply byte %d", b)
}

func (s *scpSender) sendDir(name string) error {
	if _, err := fmt.Fprintf(s.peer, "D0755 0 %s\n", name); err != nil {
		return fmt.Errorf("scp: send dir %s: %w", name, err)
	}
	return s.readAck()
}

func (s *scpSender) endDir() error {
	if _, err := io.WriteString(s.peer, "E\n"); err != nil {
		return fmt.Errorf("scp: end dir: %w", err)
	}
	return s.readAck()
}

func (s *scpSender) sendFile(path, name string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(s.peer, "C0644 %d %s\n", st.Size(), name); err != nil {
		return fmt.Errorf("scp: send header %s: %w", name, err)
	}
	if err := s.readAck(); err != nil {
		return err
	}
	if _, err := io.Copy(s.peer, f); err != nil {
		return fmt.Errorf("scp: send data %s: %w", name, err)
	}
	if _, err := io.WriteString(s.peer, "\x00"); err != nil {
		return fmt.Errorf("scp: send end of %s: %w", name, err)
	}
	if err := s.readAck(); err != nil {
		return err
	}
	s.files++
	s.bytes += st.Size()
	return nil
}

// sendTree uploads the contents of dir into the current remote directory.
func (s *scpSender) sendTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		switch {
		case entry.IsDir():
			if err := s.sendDir(name); err != nil {
				return err
			}
			if err := s.sendTree(path); err != nil {
				return err
			}
			if err := s.endDir(); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			if err := s.sendFile(path, name); err != nil {
				return err
			}
		}
	}
	return nil
}
