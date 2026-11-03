package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var sshkkeysAlgo = []string{
	"diffie-hellman-group1-sha1",
	"diffie-hellman-group14-sha1",
	"ecdh-sha2-nistp256",
	"ecdh-sha2-nistp384",
	"ecdh-sha2-nistp521",
	"diffie-hellman-group-exchange-sha1",
	"diffie-hellman-group-exchange-sha256",
	"curve25519-sha256@libssh.org",
}
var sshChippers = []string{
	"aes128-gcm@openssh.com",
	"cast128-cbc",
	"aes128-ctr", "aes192-ctr", "aes256-ctr",
	"3des-cbc", "blowfish-cbc", "twofish-cbc", "twofish256-cbc", "twofish192-cbc", "twofish128-cbc", "aes256-cbc", "aes192-cbc", "aes128-cbc", "arcfour",
}

// SshClientConfig builds the ssh client configuration for a device.
func SshClientConfig(device *Device) (*ssh.ClientConfig, error) {
	auth, err := SshClientDeviceAuth(device)
	if err != nil {
		return nil, err
	}
	device.LogDebug(fmt.Sprintf("SshClientConfig: Get auth (%t)", auth))

	return &ssh.ClientConfig{
		Config: ssh.Config{
			KeyExchanges: sshkkeysAlgo,
			Ciphers:      sshChippers,
			MACs:         sshLegacyMACs,
		},
		User:              device.Username,
		Auth:              auth,
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           time.Duration(device.Timeout) * time.Second,
		HostKeyAlgorithms: sshLegacyHostKeyAlgos,
	}, nil
}

// SshClientRun fetches the configuration over SSH.
//
// The normal path uses an exec channel. Old switches (Eltex MES and relatives)
// answer nothing that way and only work in an interactive shell, so when exec
// returns no data the interactive mode is used instead (or right away when the
// device has "sshmode": "shell").
func SshClientRun(device *Device) ([]byte, error) {
	mode := normalizeSSHMode(device.SSHMode)
	if mode == sshModeShell {
		return SshShellRun(device)
	}

	data, err := sshExecRun(device)
	if err == nil && len(bytes.TrimSpace(data)) > 0 {
		return data, nil
	}
	if mode == sshModeExec {
		return data, err
	}
	// auto mode: fall back to the interactive CLI when exec produced nothing
	if err == nil || isSSHExitMissing(err) {
		device.LogWarn(fmt.Sprintf("SshClientRun: exec channel returned no data for device %s, switching to the interactive shell", device.Name))
		return SshShellRun(device)
	}
	return data, err
}

// sshExecRun runs device.Command over an exec channel.
func sshExecRun(device *Device) ([]byte, error) {
	config, err := SshClientConfig(device)
	if err != nil {
		return []byte{}, err
	}
	device.LogDebug(fmt.Sprintf("SshClientRun: Get auth methods (%d)", len(config.Auth)))

	address := DeviceAddressFormat(device)
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, errors.New("SshClientRun: DialSSH error:" + sshDialHint(err))
	}
	defer client.Close()
	device.LogDebug(fmt.Sprintf("SshClientRun: ssh Dial running %s", device.Name))
	session, err := client.NewSession()
	if err != nil {
		return nil, errors.New("SshClientRun: NewSession error:" + err.Error())
	}
	defer session.Close()
	device.LogDebug(fmt.Sprintf("SshClientRun: ssh session running %s", device.Name))
	var b, errB bytes.Buffer
	session.Stdout = &b
	session.Stderr = &errB

	if err := session.Run(device.Command); err != nil {
		if isSSHExitMissing(err) {
			// The device closed the channel without an exit status; the output
			// that arrived is complete and valid.
			device.LogDebug("SshClientRun: device sent no exit status, keeping the received data")
			if cerr := checkCommandResult(b.Bytes()); cerr != nil {
				return nil, cerr
			}
			return b.Bytes(), nil
		}
		// A non-zero exit status is a real failure: the device usually explains
		// itself ("bad parameter ...", "Command not found"), and without that
		// text the log only says "Process exited with status 1".
		return nil, errors.New("SshClientRun: Run command error:" + err.Error() + sshErrorDetail(b.Bytes(), errB.Bytes()))
	}
	device.LogDebug(fmt.Sprintf("SshClientRun: Read bytes good %s", device.Name))
	if err := checkCommandResult(b.Bytes()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// sshErrorDetail renders the device reply for a command that failed with a
// non-zero exit status, so the log shows why and not only the exit code.
func sshErrorDetail(stdout, stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		text = strings.TrimSpace(string(stdout))
	}
	if text == "" {
		return ""
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return ": " + text
}

// sshDialHint adds a hint to the most common handshake failures.
func sshDialHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return msg + " (the device rejected the credentials: check username/password and that the user is allowed to use ssh)"
	case strings.Contains(msg, "no common algorithm"):
		return msg + " (the device only offers deprecated crypto; this is reported above)"
	case strings.Contains(msg, "unsupported DSA key size"):
		return msg + " (the device uses a 512-bit DSA host key, which Go's ssh cannot handle; use telnet for this device)"
	}
	return msg
}

// prepare auth method for client
func SshClientDeviceAuth(device *Device) ([]ssh.AuthMethod, error) {
	var auth []ssh.AuthMethod
	if device.Authkey == false {
		if device.Password == "" {
			return nil, errors.New("SshClientDeviceAuth: Password empty")
		}
		// password first, then keyboard-interactive: some devices answer only
		// the latter ("Password:" is asked by the server, not by the client).
		auth = append(auth, ssh.Password(device.Password))
		auth = append(auth, ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = device.Password
			}
			return answers, nil
		}))
		return auth, nil
	}

	flp := fmt.Sprintf("%s/%s", ConfigV.GetString("path.key"), device.Key)
	key, err := ioutil.ReadFile(flp)
	if err != nil {
		return nil, errors.New("SshClientDeviceAuth: read key file:" + err.Error())
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, errors.New("SshClientDeviceAuth: parse private file:" + err.Error())
	}
	auth = append(auth, ssh.PublicKeys(signer))
	return auth, nil
}

func DeviceAddressFormat(device *Device) string {
	port := device.Port
	if port == 0 {
		// Device did not go through SetDefaultParameter (tests, manual calls).
		port = device.PortSSH
		if normalizeProtocol(device.Protocol) == ProtocolTelnet {
			port = device.PortTelnet
			if port == 0 {
				port = telnetDefaultPort
			}
		}
		if port == 0 {
			port = 22
		}
	}
	str := fmt.Sprintf("%s:%d", device.Address, port)
	device.LogDebug(fmt.Sprintf("DeviceAddressFormat: addres (%s)", str))
	return str
}
