package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sinkPeer emulates the remote side of "scp -t": it parses the commands,
// stores the files and answers with the protocol acks.
type sinkPeer struct {
	acks      bytes.Buffer
	files     map[string][]byte
	dirs      []string
	state     int
	line      []byte
	remain    int
	name      string
	data      []byte
	stack     []string
	rejectC   bool
	rejectMsg string
}

func newSinkPeer() *sinkPeer {
	return &sinkPeer{files: map[string][]byte{}}
}

func (s *sinkPeer) Read(p []byte) (int, error) { return s.acks.Read(p) }

func (s *sinkPeer) Write(p []byte) (int, error) {
	for _, b := range p {
		switch s.state {
		case 0: // command line
			if b == '\n' {
				s.handleLine(string(s.line))
				s.line = nil
			} else {
				s.line = append(s.line, b)
			}
		case 1: // file data
			s.data = append(s.data, b)
			s.remain--
			if s.remain == 0 {
				s.state = 2
			}
		case 2: // trailing NUL of the file
			if b != 0 {
				return 0, fmt.Errorf("expected NUL after file data, got %d", b)
			}
			s.files[s.currentPath()] = s.data
			s.data = nil
			s.state = 0
			s.acks.WriteByte(0)
		}
	}
	return len(p), nil
}

func (s *sinkPeer) currentPath() string {
	parts := append(append([]string{}, s.stack...), s.name)
	return strings.Join(parts, "/")
}

func (s *sinkPeer) handleLine(line string) {
	fields := strings.Fields(line)
	switch {
	case strings.HasPrefix(line, "D"):
		s.stack = append(s.stack, fields[2])
		s.dirs = append(s.dirs, strings.Join(s.stack, "/"))
		s.acks.WriteByte(0)
	case strings.HasPrefix(line, "C"):
		if s.rejectC {
			s.acks.Write([]byte{1})
			s.acks.WriteString(s.rejectMsg + "\n")
			return
		}
		size, err := strconv.Atoi(fields[1])
		if err != nil {
			s.acks.Write([]byte{1})
			s.acks.WriteString("bad size\n")
			return
		}
		s.name = fields[2]
		s.remain = size
		s.acks.WriteByte(0)
		if size == 0 {
			s.state = 2
		} else {
			s.state = 1
		}
	case line == "E":
		if len(s.stack) > 0 {
			s.stack = s.stack[:len(s.stack)-1]
		}
		s.acks.WriteByte(0)
	default:
		s.acks.Write([]byte{1})
		s.acks.WriteString("unknown command\n")
	}
}

func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "devA"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "devB", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "empty"), 0o755))
	must(os.WriteFile(filepath.Join(root, "devA", "20260101.rcs"), []byte("aaa"), 0o644))
	must(os.WriteFile(filepath.Join(root, "devB", "sub", "20260101.rcs"), []byte("bbbb"), 0o644))
	must(os.WriteFile(filepath.Join(root, "devB", "zero.rcs"), nil, 0o644))
	return root
}

func TestSCPSendTree(t *testing.T) {
	root := makeTree(t)
	sink := newSinkPeer()
	sender := newSCPSender(sink)

	if err := sender.sendTree(root); err != nil {
		t.Fatalf("sendTree: %v", err)
	}

	if sender.files != 3 {
		t.Fatalf("files sent = %d, want 3", sender.files)
	}
	if sender.bytes != int64(len("aaa")+len("bbbb")) {
		t.Fatalf("bytes sent = %d", sender.bytes)
	}

	want := map[string]string{
		"devA/20260101.rcs":     "aaa",
		"devB/zero.rcs":         "",
		"devB/sub/20260101.rcs": "bbbb",
	}
	if len(sink.files) != len(want) {
		t.Fatalf("remote got %d files, want %d: %v", len(sink.files), len(want), keysOf(sink.files))
	}
	for name, content := range want {
		got, ok := sink.files[name]
		if !ok {
			t.Fatalf("remote is missing %q, got %v", name, keysOf(sink.files))
		}
		if string(got) != content {
			t.Fatalf("%q: got %q, want %q", name, got, content)
		}
	}

	foundEmpty := false
	for _, d := range sink.dirs {
		if d == "empty" {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Fatalf("empty directory was not created, dirs: %v", sink.dirs)
	}
}

func TestSCPSendTreeRemoteError(t *testing.T) {
	root := makeTree(t)
	sink := newSinkPeer()
	sink.rejectC = true
	sink.rejectMsg = "scp: /destination: Permission denied"

	err := newSCPSender(sink).sendTree(root)
	if err == nil {
		t.Fatal("expected an error when the remote rejects the file")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("remote message must be reported, got %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/volume1/backups/pupirka":  "'/volume1/backups/pupirka'",
		"/volume1/back ups/pupirka": "'/volume1/back ups/pupirka'",
		"/volume1/it's/pupirka":     `'/volume1/it'\''s/pupirka'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUploadAuth(t *testing.T) {
	if _, err := uploadAuth(uploadConfig{}); err == nil || !strings.Contains(err.Error(), "upload.key") {
		t.Fatalf("missing key and password must be reported, got %v", err)
	}

	if _, err := uploadAuth(uploadConfig{Key: "no-such-key-here"}); err == nil || !strings.Contains(err.Error(), "read key") {
		t.Fatalf("missing key file must be reported, got %v", err)
	}

	methods, err := uploadAuth(uploadConfig{Password: "secret"})
	if err != nil || len(methods) != 1 {
		t.Fatalf("password auth: methods=%d err=%v", len(methods), err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
