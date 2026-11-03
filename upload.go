package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// uploadConfig is the "upload" section of pupirka.config.json.
type uploadConfig struct {
	Enabled  bool
	Address  string
	Port     uint16
	Username string
	Password string
	Key      string
	Path     string
	Timeout  int
}

// uploadConfigFromViper reads the upload section.
func uploadConfigFromViper() uploadConfig {
	port := ConfigV.GetInt("upload.port")
	if port <= 0 || port > 65535 {
		port = 22
	}
	timeout := ConfigV.GetInt("upload.timeout")
	if timeout <= 0 {
		timeout = 120
	}
	return uploadConfig{
		Enabled:  ConfigV.GetBool("upload.enabled"),
		Address:  strings.TrimSpace(ConfigV.GetString("upload.address")),
		Port:     uint16(port),
		Username: ConfigV.GetString("upload.username"),
		Password: ConfigV.GetString("upload.password"),
		Key:      strings.TrimSpace(ConfigV.GetString("upload.key")),
		Path:     strings.TrimSpace(ConfigV.GetString("upload.path")),
		Timeout:  timeout,
	}
}

// shellQuote quotes a path for the remote shell (the scp command and mkdir are
// executed by a shell on the other side).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// uploadAuth builds the auth methods for the upload host: a private key from
// the keys directory first, then a password.
func uploadAuth(cfg uploadConfig) ([]ssh.AuthMethod, error) {
	if cfg.Key != "" {
		keyPath := filepath.Join(ConfigV.GetString("path.key"), cfg.Key)
		key, err := ioutil.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("upload: read key %s: %w", keyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("upload: parse key %s: %w", keyPath, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if cfg.Password == "" {
		return nil, errors.New(`upload: set "upload.key" (a file in the keys directory) or "upload.password"`)
	}
	return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
}

func uploadSSHClient(cfg uploadConfig) (*ssh.Client, error) {
	auth, err := uploadAuth(cfg)
	if err != nil {
		return nil, err
	}
	clientConfig := &ssh.ClientConfig{
		Config: ssh.Config{
			KeyExchanges: sshkkeysAlgo,
			Ciphers:      sshChippers,
			MACs:         sshLegacyMACs,
		},
		User:              cfg.Username,
		Auth:              auth,
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           time.Duration(cfg.Timeout) * time.Second,
		HostKeyAlgorithms: sshLegacyHostKeyAlgos,
	}
	address := net.JoinHostPort(cfg.Address, fmt.Sprintf("%d", cfg.Port))
	client, err := ssh.Dial("tcp", address, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("upload: dial %s: %w", address, err)
	}
	return client, nil
}

// RunUpload copies the whole backup directory to the upload host over ssh.
//
// It runs after the backups and after the git push, so the server receives the
// same tree that was committed. A failure is returned to the caller, which logs
// it: the run itself is not aborted, exactly like the git push.
func RunUpload() error {
	cfg := uploadConfigFromViper()
	if !cfg.Enabled {
		return nil
	}
	if cfg.Address == "" || cfg.Path == "" {
		return errors.New(`upload: "upload.address" and "upload.path" are required`)
	}

	localRoot := ConfigV.GetString("path.backup")
	if st, err := os.Stat(localRoot); err != nil {
		return fmt.Errorf("upload: local directory %q: %w", localRoot, err)
	} else if !st.IsDir() {
		return fmt.Errorf("upload: local path %q is not a directory", localRoot)
	}

	client, err := uploadSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	remotePath := strings.TrimRight(cfg.Path, "/")
	if remotePath == "" {
		remotePath = "/"
	}
	if err := uploadMkdir(client, remotePath); err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("upload: new session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("upload: stdin: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("upload: stdout: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	LogConsole.Info(fmt.Sprintf("Upload: %s -> %s@%s:%s", localRoot, cfg.Username, cfg.Address, remotePath))
	// -r is required by the sink side: without it the remote scp refuses to
	// create directories ("protocol error: received directory without -r").
	if err := session.Start("scp -t -r " + shellQuote(remotePath)); err != nil {
		return fmt.Errorf("upload: start scp: %w", err)
	}

	// The remote scp can hang (no answer, full disk): close the session when
	// the configured timeout expires.
	timer := time.AfterFunc(time.Duration(cfg.Timeout)*time.Second, func() {
		_ = session.Close()
	})
	defer timer.Stop()

	sender := newSCPSender(&sshScpPeer{writer: stdin, reader: stdout})
	sendErr := sender.sendTree(localRoot)

	_ = stdin.Close()
	waitErr := session.Wait()

	if sendErr != nil {
		return withSCPStderr("upload: "+sendErr.Error(), stderr.Bytes())
	}
	if waitErr != nil {
		return withSCPStderr("upload: scp failed: "+waitErr.Error(), stderr.Bytes())
	}
	LogConsole.Info(fmt.Sprintf("Upload: done, %d files, %d bytes", sender.files, sender.bytes))
	return nil
}

// uploadMkdir creates the destination directory (scp -t requires it to exist).
func uploadMkdir(client *ssh.Client, remotePath string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("upload: new session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run("mkdir -p " + shellQuote(remotePath)); err != nil {
		return withSCPStderr("upload: mkdir "+remotePath+": "+err.Error(), stderr.Bytes())
	}
	return nil
}

// withSCPStderr appends the first line the remote side printed.
func withSCPStderr(msg string, stderr []byte) error {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return errors.New(msg)
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	return fmt.Errorf("%s: %s", msg, text)
}

// sshScpPeer joins the two ssh pipes into one scpPeer.
type sshScpPeer struct {
	writer io.WriteCloser
	reader io.Reader
}

func (p *sshScpPeer) Read(b []byte) (int, error)  { return p.reader.Read(b) }
func (p *sshScpPeer) Write(b []byte) (int, error) { return p.writer.Write(b) }
