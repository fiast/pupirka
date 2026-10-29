package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// chunkConn feeds predefined chunks to Read and records Write.
type chunkConn struct {
	chunks [][]byte
	writes bytes.Buffer
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	ch := c.chunks[0]
	n := copy(p, ch)
	if n == len(ch) {
		c.chunks = c.chunks[1:]
	} else {
		c.chunks[0] = ch[n:]
	}
	return n, nil
}

func (c *chunkConn) Write(p []byte) (int, error)      { return c.writes.Write(p) }
func (c *chunkConn) Close() error                     { return nil }
func (c *chunkConn) LocalAddr() net.Addr              { return nil }
func (c *chunkConn) RemoteAddr() net.Addr             { return nil }
func (c *chunkConn) SetDeadline(time.Time) error      { return nil }
func (c *chunkConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chunkConn) SetWriteDeadline(time.Time) error { return nil }

func TestTelnetConnStripsNegotiation(t *testing.T) {
	c := &chunkConn{chunks: [][]byte{
		[]byte("abc\xff"),                  // IAC split across chunks
		[]byte("\xfb\x01def"),              // WILL ECHO
		[]byte("\xff\xfa\x18\x01\x02\xff"), // SB NAWS ...
		[]byte("\xf0ghi"),                  // ... IAC SE
		[]byte("\xff\xfd\x1f"),             // DO NAWS
		[]byte("j\xff\xffk"),               // escaped 0xFF in the data
	}}

	got, err := io.ReadAll(newTelnetConn(c))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "abcdefghi" + "j" + string([]byte{0xff}) + "k"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTelnetConnNeverAnswersNegotiation(t *testing.T) {
	c := &chunkConn{chunks: [][]byte{
		[]byte("\xff\xfb\x01\xff\xfb\x03\xff\xfe\x22\xff\xfd\x1f"),
		[]byte("User name:"),
	}}
	if _, err := io.ReadAll(newTelnetConn(c)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if c.writes.Len() != 0 {
		t.Fatalf("the client must stay silent, but wrote %q", c.writes.String())
	}
}

func TestTelnetConnEscapesIACOnWrite(t *testing.T) {
	c := &chunkConn{}
	tc := newTelnetConn(c)

	n, err := tc.Write([]byte("a\xffb"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 3 {
		t.Fatalf("n = %d, want 3", n)
	}
	if got := c.writes.String(); got != "a\xff\xffb" {
		t.Fatalf("got %q, want %q", got, "a\xff\xffb")
	}

	c.writes.Reset()
	if _, err := tc.Write([]byte("plain\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := c.writes.String(); got != "plain\r" {
		t.Fatalf("plain data must pass untouched, got %q", got)
	}
}

func TestTelnetConnSubnegotiationWithEscapedIAC(t *testing.T) {
	// TTYPE subnegotiation containing an escaped 0xFF must not swallow data
	c := &chunkConn{chunks: [][]byte{
		[]byte("\xff\xfa\x18\xff\xff\x01\xff\xf0ok"),
	}}
	got, err := io.ReadAll(newTelnetConn(c))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q, want %q", got, "ok")
	}
}
