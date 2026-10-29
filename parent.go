package main

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

var MDeviceList = make(map[string]Device)

var (
	mLocalPortMu sync.Mutex
	MLocalPort   = make(map[uint16]string)
)

// SshForwardNewDevice starts a local TCP listener whose connections are
// tunnelled to child through the SSH parent (ssh.Dial + conn.Dial), so the
// caller can reach child on localhost:<port>.
//
// It returns a copy of child with Address/Port pointing at the local listener,
// a stop function that closes the listener and frees the port, and an error.
//
// The listener is created synchronously on purpose: the caller dials it right
// away, and a goroutine that is still starting would make that dial fail with
// "connection refused" from time to time.
func SshForwardNewDevice(parent Device, child Device) (Device, func(), error) {
	child.LogDebug("SshForwardNewDevice: Get new address", child.Name)
	lport := SshLocalGeneratePort()
	localAddr := fmt.Sprintf("localhost:%d", lport)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		SshLocalReleasePort(lport)
		return child, func() {}, fmt.Errorf("SshForwardNewDevice: listen %s: %w", localAddr, err)
	}
	child.LogDebug(fmt.Sprintf("SshForwardNewDevice: forward %s -> %s:%d", localAddr, child.Address, child.Port))

	done := make(chan struct{})
	// child is passed by value here: the goroutine keeps the real device
	// address, while the returned copy below points at the tunnel.
	go SshClientRunForward(&parent, child, listener, done)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			_ = listener.Close()
			SshLocalReleasePort(lport)
		})
	}

	tunneled := child
	tunneled.Address = "localhost"
	tunneled.Port = lport
	child.LogDebug(fmt.Sprintf("SshForwardNewDevice: new address (%s), new port (%d)", tunneled.Address, tunneled.Port))
	return tunneled, stop, nil
}

// SshClientRunForward accepts local connections and tunnels each of them to child.
func SshClientRunForward(parent *Device, child Device, listener net.Listener, done <-chan struct{}) {
	config, cfgErr := SshClientConfig(parent)
	if cfgErr != nil {
		child.LogError("SshClientRunForward: cannot build ssh client config for parent: ", cfgErr.Error())
		return
	}
	parentAddr := DeviceAddressFormat(parent)
	childAddr := DeviceAddressFormat(&child)
	child.LogDebug(fmt.Sprintf("SshClientRunForward: tunnel %s -> %s", parentAddr, childAddr))

	go func() {
		<-done
		_ = listener.Close()
	}()

	for {
		localConn, err := listener.Accept()
		if err != nil {
			select {
			case <-done:
				return // listener closed by stop(), not an error
			default:
			}
			child.LogError(fmt.Sprintf("SshClientRunForward listen.Accept failed: %s", err.Error()))
			return
		}
		go forward(localConn, config, parentAddr, childAddr)
	}
}

// forward splices one local connection with the ssh channel to the child.
func forward(localConn net.Conn, config *ssh.ClientConfig, parentaddress string, childaddress string) {
	defer localConn.Close()

	sshClientConn, err := ssh.Dial("tcp", parentaddress, config)
	if err != nil {
		LogConsole.Info(fmt.Sprintf("ssh.Dial failed %s ... Child %s, Parent %s", err.Error(), childaddress, parentaddress))
		return
	}
	defer sshClientConn.Close()

	sshConn, err := sshClientConn.Dial("tcp", childaddress)
	if err != nil {
		LogConsole.Info(fmt.Sprintf("ssh forward to child failed %s ... Child %s, Parent %s", err.Error(), childaddress, parentaddress))
		return
	}
	defer sshConn.Close()

	// Copy localConn.Reader to sshConn.Writer
	go func() {
		if _, err := io.Copy(sshConn, localConn); err != nil {
			LogConsole.Debug(fmt.Sprintf("io.Copy to child failed %s ... Child %s, Parent %s", err.Error(), childaddress, parentaddress))
		}
		_ = sshConn.Close()
		_ = localConn.Close()
	}()

	// Copy sshConn.Reader to localConn.Writer
	if _, err := io.Copy(localConn, sshConn); err != nil {
		LogConsole.Debug(fmt.Sprintf("io.Copy from child failed %s ... Child %s, Parent %s", err.Error(), childaddress, parentaddress))
	}
}

func SshLocalGeneratePort() uint16 {
	min := 40000
	max := 50000

	mLocalPortMu.Lock()
	defer mLocalPortMu.Unlock()
	for {
		r := rand.Intn(max-min+1) + min
		if _, ok := MLocalPort[uint16(r)]; ok {
			continue
		}
		MLocalPort[uint16(r)] = ""
		return uint16(r)
	}
}

// SshLocalReleasePort frees a tunnel port so it can be reused by other devices.
func SshLocalReleasePort(port uint16) {
	mLocalPortMu.Lock()
	delete(MLocalPort, port)
	mLocalPortMu.Unlock()
}

func SshNeedForward(device *Device) (Device, Device, error) {

	if _, ok := MDeviceList[device.Parent]; !ok {
		return Device{}, Device{}, errors.New("SshNeedForward: no isset parent device")
	}
	parent := MDeviceList[device.Parent]
	dev := *device
	return parent, dev, nil
}
