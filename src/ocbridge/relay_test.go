package main

import (
	"bytes"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// pair returns the two ends of an AF_UNIX SOCK_DGRAM socketpair as UnixConns,
// mirroring how ocbridge adopts both openconnect's VPNFD and openvpn's tun.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, err := fdConn(fds[0], "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := fdConn(fds[1], "b")
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestRelayPreservesPacketBoundaries(t *testing.T) {
	vpnFar, vpnNear := pair(t) // vpnFar stands in for openconnect
	tunNear, tunFar := pair(t) // tunFar stands in for openvpn
	defer vpnFar.Close()
	defer tunFar.Close()

	var c Counters
	go func() { _ = Relay(vpnNear, tunNear, &c) }()

	// Distinct sizes: a datagram socket that silently coalesced or split
	// packets would show up here, and every packet is a whole IP packet.
	sizes := []int{20, 576, 1434, 40}
	for i, n := range sizes {
		want := bytes.Repeat([]byte{byte(i + 1)}, n)
		if _, err := vpnFar.Write(want); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, maxPacket)
		_ = tunFar.SetReadDeadline(time.Now().Add(3 * time.Second))
		got, err := tunFar.Read(buf)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if got != n || !bytes.Equal(buf[:got], want) {
			t.Fatalf("packet %d: got %d bytes, want %d (intact=%v)",
				i, got, n, bytes.Equal(buf[:got], want))
		}
	}

	// ...and the other direction.
	probe := []byte("client-to-gateway")
	if _, err := tunFar.Write(probe); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, maxPacket)
	_ = vpnFar.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := vpnFar.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], probe) {
		t.Fatalf("reverse direction got %q", buf[:n])
	}

	var total int
	for _, n := range sizes {
		total += n
	}
	// Counters are read by the UI; make sure they actually tally.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && c.FromVPN.Load() < uint64(total) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := c.FromVPN.Load(); got != uint64(total) {
		t.Errorf("FromVPN = %d, want %d", got, total)
	}
	if got := c.ToVPN.Load(); got != uint64(len(probe)) {
		t.Errorf("ToVPN = %d, want %d", got, len(probe))
	}
}

// Closing OUR end stops the relay. This is the path ocbridge actually uses to
// shut down, because the peer-close path does not work (see below).
func TestRelayStopsWhenOurEndCloses(t *testing.T) {
	vpnFar, vpnNear := pair(t)
	tunNear, tunFar := pair(t)
	defer vpnFar.Close()
	defer tunFar.Close()

	done := make(chan error, 1)
	var c Counters
	go func() { done <- Relay(vpnNear, tunNear, &c) }()

	vpnNear.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Relay did not return after its own end was closed")
	}
}

// Pin down the kernel behaviour that forces ocbridge to use PR_SET_PDEATHSIG:
// on Linux, closing the far end of an AF_UNIX SOCK_DGRAM socketpair does NOT
// wake a blocked reader. There is no EOF and no POLLHUP, unlike SOCK_STREAM.
//
// If this test ever starts failing, the kernel or Go changed and the liveness
// story in DieWithParent can be simplified. Until then, nothing in the relay
// may assume a dead peer is detectable from the socket.
func TestPeerCloseIsNotDetectableOnADatagramSocket(t *testing.T) {
	far, near := pair(t)
	defer near.Close()

	far.Close()
	_ = near.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, err := near.Read(make([]byte, 64))
	ne, ok := err.(net.Error)
	if !ok || !ne.Timeout() {
		t.Fatalf("peer close became detectable: n=%d err=%v -- the relay's "+
			"liveness assumptions can be revisited", n, err)
	}
}

func TestFdConnRejectsSomethingThatIsNotASocket(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notasocket")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fdConn(int(f.Fd()), "file"); err == nil {
		t.Fatal("fdConn accepted a regular file")
	}
}

func TestStatusPublisherWritesPrivateAtomicJSON(t *testing.T) {
	path := t.TempDir() + "/status.json"
	p := NewPublisher(path)
	if err := p.Update(func(s *Status) { s.State = "up"; s.VPNPass = "secret" }); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// It carries the local VPN password, so it must not be world readable.
	if st.Mode().Perm() != 0o600 {
		t.Errorf("status.json mode %v, want 0600", st.Mode().Perm())
	}
	if got := p.Snapshot(); got.State != "up" {
		t.Errorf("snapshot state = %q", got.State)
	}
}

func TestRuntimeDirDoesNotNestUnderConfinement(t *testing.T) {
	const pkg = "ocvpn.franzthiemann"
	// Lomiri already scopes XDG_RUNTIME_DIR by package for confined apps.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/32011/confined/"+pkg)
	if got := RuntimeDir(pkg, "/data"); got != "/run/user/32011/confined/"+pkg {
		t.Errorf("RuntimeDir = %q, want the unchanged confined path", got)
	}
	// An unscoped runtime dir still gets the package appended.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := RuntimeDir(pkg, "/data"); got != "/run/user/1000/"+pkg {
		t.Errorf("RuntimeDir = %q", got)
	}
	// And with none at all we stay inside the app's own data directory.
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := RuntimeDir(pkg, "/data"); got != "/data/run" {
		t.Errorf("RuntimeDir = %q", got)
	}
}
