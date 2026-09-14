package main

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// Counters is the byte tally the UI shows. Read with the atomic loads.
type Counters struct {
	ToVPN   atomic.Uint64 // client -> Cisco
	FromVPN atomic.Uint64 // Cisco -> client
}

// maxPacket is comfortably above any tunnel MTU. A short read on a datagram
// socket TRUNCATES rather than returning the rest, so this must never be tight.
const maxPacket = 65535

// fdConn adopts an already-open descriptor as a connected unix datagram socket.
// os.NewFile does not dup, but net.FileConn does, so the original descriptor is
// closed afterwards to avoid leaking it into the openvpn child.
func fdConn(fd int, name string) (*net.UnixConn, error) {
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		return nil, errors.New("not a valid descriptor: " + name)
	}
	defer f.Close()
	c, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, errors.New(name + " is not a unix socket")
	}
	return uc, nil
}

// Relay copies IP packets between the two ends until either side fails.
//
// Both ends are AF_UNIX/SOCK_DGRAM and both carry bare IP packets with no
// framing: openconnect suppresses the BSD-style 4-byte AF prefix in script-tun
// mode, and Linux openvpn opens its tun with IFF_NO_PI. So this is a straight
// copy, one datagram in, one datagram out -- no length prefixing and no
// re-framing. (Relaying over a pipe or pty instead would need re-framing on the
// IPv4 total-length field, which is why that approach is fragile.)
func Relay(vpn, tun *net.UnixConn, c *Counters) error {
	var once sync.Once
	var first error
	done := make(chan struct{})

	finish := func(err error) {
		once.Do(func() {
			first = err
			close(done)
		})
	}

	pump := func(dst, src *net.UnixConn, tally *atomic.Uint64) {
		buf := make([]byte, maxPacket)
		for {
			n, err := src.Read(buf)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				finish(err)
				return
			}
			if n == 0 {
				// A zero-length read means the peer is gone. On a DATAGRAM
				// socket Go does not translate that into io.EOF the way it
				// does for streams -- net/fd_unix.go sets ZeroReadIsEOF only
				// for non-SOCK_DGRAM -- so Read returns (0, nil) forever once
				// the far end closes. Treating it as "continue" is an
				// infinite busy loop that leaves a dead tunnel looking alive.
				// A genuine zero-length IP packet does not exist, so this is
				// unambiguous here.
				finish(nil)
				return
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				finish(err)
				return
			}
			tally.Add(uint64(n))
		}
	}

	go pump(tun, vpn, &c.FromVPN)
	go pump(vpn, tun, &c.ToVPN)

	<-done
	// Unblock the surviving goroutine so it cannot outlive the process.
	vpn.Close()
	tun.Close()
	return first
}
