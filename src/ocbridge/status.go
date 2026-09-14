package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Status is what the app reads to render the connection state. It is published
// as a JSON file rather than over a socket, deliberately: the tunnel outlives
// the UI (Lomiri suspends unfocused apps, so ocbridge runs detached in its own
// session), and a file is still there to be read when the app is restarted and
// has to re-attach to a tunnel that is already up.
type Status struct {
	State     string   `json:"state"` // starting | up | down | error
	Error     string   `json:"error,omitempty"`
	Since     int64    `json:"since"`   // unix seconds
	Address   string   `json:"address"` // the gateway-assigned address
	Gateway   string   `json:"gateway"`
	MTU       int      `json:"mtu"`
	DNS       []string `json:"dns,omitempty"`
	Domain    string   `json:"domain,omitempty"`
	Routes    []string `json:"routes,omitempty"`
	Port      int      `json:"port"`
	Proto     string   `json:"proto"`
	VPNUser   string   `json:"vpn_user"` // credentials for the local OpenVPN client
	VPNPass   string   `json:"vpn_pass"`
	CACert    string   `json:"ca_cert"` // the one file the user must pick in Settings
	BytesIn   uint64   `json:"bytes_in"`
	BytesOut  uint64   `json:"bytes_out"`
	ClientUp  bool     `json:"client_up"` // has the local OpenVPN client connected?
	UpdatedAt int64    `json:"updated_at"`
}

// Publisher writes Status atomically so a reader never sees a partial file.
type Publisher struct {
	path string
	mu   sync.Mutex
	cur  Status
}

func NewPublisher(path string) *Publisher { return &Publisher{path: path} }

func (p *Publisher) Update(fn func(*Status)) error {
	p.mu.Lock()
	fn(&p.cur)
	p.cur.UpdatedAt = time.Now().Unix()
	data, err := json.MarshalIndent(&p.cur, "", "  ")
	p.mu.Unlock()
	if err != nil {
		return err
	}
	// The password for the local OpenVPN client lives in here, so keep it 0600.
	// Ubuntu Touch has no keyring; the app's confined data directory is the
	// best available, and this is a credential for a loopback listener only.
	return writeFile(p.path, append(data, '\n'), 0o600)
}

func (p *Publisher) Snapshot() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

// WritePidFile records our pid so the app can find and stop a tunnel it did not
// start itself. Disconnect is a SIGTERM to this pid.
func WritePidFile(path string) error {
	return writeFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// RuntimeDir is where the pidfile and status file live: volatile state that
// must not survive a reboot. Falls back to the data dir when XDG_RUNTIME_DIR is
// unset, which is the case under `adb shell`.
//
// Verified on a Fairphone 5 running 24.04-1.x: a confined app sees
// XDG_RUNTIME_DIR=/run/user/32011 (NOT the confined/<package> path), and the
// generated profile grants
//
//	owner /{,var/}run/user/*/@{APP_PKGNAME}/   rw
//	owner /{,var/}run/user/*/@{APP_PKGNAME}/** mrwkl
//
// so <runtime>/<package> is exactly right. The basename check below is
// defensive only: the profile also grants .../confined/@{APP_PKGNAME}/ but
// WITHOUT a /** rule, so if some future session pointed XDG_RUNTIME_DIR there,
// appending the package again would create a subdirectory AppArmor denies.
func RuntimeDir(pkg, fallback string) string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		return filepath.Join(fallback, "run")
	}
	if filepath.Base(d) == pkg {
		return d
	}
	return filepath.Join(d, pkg)
}

// freePort asks the kernel for an unused UDP port on loopback. The OpenVPN
// client's profile in Settings names a fixed port, so this is only used when
// the caller has not pinned one.
func freePort() (int, error) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}
