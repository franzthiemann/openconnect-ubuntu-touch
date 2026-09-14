// Command ocbridge terminates a Cisco AnyConnect tunnel in userspace and
// re-exposes it as an OpenVPN server on loopback, so that Ubuntu Touch's own
// (root) NetworkManager OpenVPN client can create the real tun device and
// install the routes and DNS. Nothing here needs root or any capability, which
// is what lets the app ship as a normal confined click.
//
// It is not run directly. openconnect execs it as its --script-tun program:
//
//	openconnect --script-tun --script /path/to/ocbridge ... https://gateway
//
// openconnect passes the tunnel over an AF_UNIX SOCK_DGRAM socketpair whose
// descriptor number is in $VPNFD, and describes the tunnel through the usual
// vpnc-script environment variables.
package main

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultPort = 1194

func main() {
	log.SetFlags(0)
	log.SetPrefix("ocbridge: ")
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// dirOfExecutable is where the click's bin/ lives: ocbridge, the patched
// openvpn and the auth helper all ship side by side, and all three are inside
// the package tree, which is the only place a confined app may execute from.
func dirOfExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func lookupTool(dir, name, envVar string) (string, error) {
	if p := os.Getenv(envVar); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q: %w", envVar, p, err)
		}
		return p, nil
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("cannot find %s next to the executable (%s): %w", name, p, err)
	}
	return p, nil
}

func run() error {
	dataDir := flag.String("data-dir", "", "where the CA, credentials and config live")
	port := flag.Int("port", 0, "UDP port for the local OpenVPN server")
	initOnly := flag.Bool("init", false,
		"generate the CA and local credentials, print them as JSON, and exit")
	proto := flag.String("proto", "", "transport for the local OpenVPN server: tcp or udp")
	authCheck := flag.Bool("auth", false,
		"verify an openvpn auth-user-pass-verify file (internal)")
	flag.Parse()

	if *authCheck {
		return runAuthCheck(flag.Args())
	}

	if *initOnly {
		return runInit(*dataDir, *port, resolveProto(*proto))
	}

	p, err := ParseParams()
	if err != nil {
		return err
	}

	// Must happen before we start relaying: see DieWithParent for why the
	// sockets alone cannot tell us that openconnect has gone.
	if err := DieWithParent(); err != nil {
		return err
	}

	_, dd, runDir, err := paths(*dataDir)
	if err != nil {
		return err
	}

	prt := *port
	if prt == 0 {
		prt, _ = strconv.Atoi(envOr("OCBRIDGE_PORT", ""))
	}
	if prt == 0 {
		prt = defaultPort
	}

	paths, err := EnsurePKI(dd)
	if err != nil {
		return fmt.Errorf("generating the local CA: %w", err)
	}
	paths.CCDDir = filepath.Join(dd, "ccd")
	paths.CredsFile = filepath.Join(dd, "vpn-creds")

	exeDir, err := dirOfExecutable()
	if err != nil {
		return err
	}
	openvpnBin, err := lookupTool(exeDir, "openvpn", "OCBRIDGE_OPENVPN")
	if err != nil {
		return err
	}
	if paths.AuthHelper, err = os.Executable(); err != nil {
		return err
	}

	vpnUser, vpnPass, err := EnsureCreds(paths.CredsFile)
	if err != nil {
		return fmt.Errorf("generating local VPN credentials: %w", err)
	}

	serverIP, err := p.ServerIP()
	if err != nil {
		return err
	}

	status := NewPublisher(filepath.Join(runDir, "status.json"))
	routes := make([]string, 0, len(p.Splits))
	for _, r := range p.Splits {
		routes = append(routes, r.Network.String()+"/"+r.Mask.String())
	}
	dns := make([]string, 0, len(p.DNS))
	for _, d := range p.DNS {
		dns = append(dns, d.String())
	}
	_ = status.Update(func(s *Status) {
		s.State, s.Since = "starting", time.Now().Unix()
		s.Address, s.MTU, s.Port = p.Address.String(), p.MTU, prt
		s.DNS, s.Domain, s.Routes = dns, p.Domain, routes
		s.VPNUser, s.VPNPass, s.CACert = vpnUser, vpnPass, paths.CACert
		s.Proto = resolveProto(*proto)
		if p.Gateway != nil {
			s.Gateway = p.Gateway.String()
		}
	})
	if err := WritePidFile(filepath.Join(runDir, "ocbridge.pid")); err != nil {
		return err
	}

	// The openvpn end of the tun. SOCK_DGRAM so packet boundaries survive,
	// exactly as a real tun device delivers them.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %w", err)
	}
	tunOurs := os.NewFile(uintptr(fds[0]), "tun-ours")
	tunTheirs := os.NewFile(uintptr(fds[1]), "tun-theirs")

	// ExtraFiles[0] becomes descriptor 3 in the child.
	const childTunFD = 3
	cfg, err := BuildServerConfig(p, paths, serverIP, childTunFD, prt, resolveProto(*proto))
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dd, "server.conf")
	if err := writeFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(paths.CCDDir, "DEFAULT"), []byte(BuildCCD(p)), 0o600); err != nil {
		return err
	}

	cmd := exec.Command(openvpnBin, "--config", cfgPath)
	cmd.ExtraFiles = []*os.File{tunTheirs}
	cmd.Stdin = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout.(io.Writer)

	logPath := filepath.Join(runDir, "openvpn.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", openvpnBin, err)
	}
	tunTheirs.Close() // the child owns it now

	go watchOpenVPN(stdout, logFile, status)

	vpnConn, err := fdConn(p.VPNFD, "vpnfd")
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("adopting VPNFD: %w", err)
	}
	tunConn, err := fdConn(int(tunOurs.Fd()), "tun")
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("adopting the tun socketpair: %w", err)
	}
	tunOurs.Close()

	var counters Counters

	// openconnect tears the script down with kill(-pid, SIGHUP) on the whole
	// process group, so SIGHUP is the normal shutdown path, not an error.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)

	relayErr := make(chan error, 1)
	go func() { relayErr <- Relay(vpnConn, tunConn, &counters) }()

	ovpnExit := make(chan error, 1)
	go func() { ovpnExit <- cmd.Wait() }()

	// Publish byte counters while we are up.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	_ = status.Update(func(s *Status) { s.State = "up" })

	var exitErr error
	shutdown := ""
loop:
	for {
		select {
		case sig := <-sigs:
			shutdown = "signal " + sig.String()
			break loop
		case err := <-relayErr:
			if err != nil {
				exitErr = fmt.Errorf("relay: %w", err)
			}
			shutdown = "tunnel closed"
			break loop
		case err := <-ovpnExit:
			exitErr = fmt.Errorf("openvpn exited: %v (see %s)", err, logPath)
			shutdown = "openvpn exited"
			break loop
		case <-tick.C:
			_ = status.Update(func(s *Status) {
				s.BytesIn = counters.FromVPN.Load()
				s.BytesOut = counters.ToVPN.Load()
			})
		}
	}

	vpnConn.Close()
	tunConn.Close()
	stopProcess(cmd, 5*time.Second)

	_ = status.Update(func(s *Status) {
		s.BytesIn, s.BytesOut = counters.FromVPN.Load(), counters.ToVPN.Load()
		s.ClientUp = false
		if exitErr != nil {
			s.State, s.Error = "error", exitErr.Error()
		} else {
			s.State, s.Error = "down", ""
		}
	})
	log.Printf("shutting down (%s)", shutdown)
	return exitErr
}

// stopProcess asks openvpn to exit, then insists. A plain SIGTERM is enough in
// practice; the kill is there so a wedged child can never keep the app's
// tunnel slot occupied.
func stopProcess(cmd *exec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
	}
}

// watchOpenVPN tees openvpn's output to a log file and lifts the few lines the
// UI cares about out of it. openvpn has no status API we can query here, so
// scraping its log is the available signal.
func watchOpenVPN(r io.Reader, logFile io.Writer, status *Publisher) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		fmt.Fprintln(logFile, line)
		switch {
		case strings.Contains(line, "Initialization Sequence Completed"):
			_ = status.Update(func(s *Status) { s.State = "up" })
		case strings.Contains(line, "Peer Connection Initiated"):
			_ = status.Update(func(s *Status) { s.ClientUp = true })
		case strings.Contains(line, "client-instance restarting") ||
			strings.Contains(line, "SIGTERM[soft,remote-exit]"):
			_ = status.Update(func(s *Status) { s.ClientUp = false })
		}
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/phablet"
	}
	return filepath.Join(home, ".local", "share")
}

// paths resolves the data and runtime directories and the package name, which
// both the tunnel path and -init need.
func paths(dataDir string) (pkg, dd, runDir string, err error) {
	pkg = envOr("OCBRIDGE_PKG", "ocvpn.franzthiemann")
	dd = dataDir
	if dd == "" {
		dd = envOr("OCBRIDGE_DATA_DIR", filepath.Join(xdgDataHome(), pkg))
	}
	runDir = RuntimeDir(pkg, dd)
	for _, d := range []string{dd, runDir, filepath.Join(dd, "ccd")} {
		if err = os.MkdirAll(d, 0o700); err != nil {
			return "", "", "", err
		}
	}
	return pkg, dd, runDir, nil
}

// SetupInfo is what the user has to reproduce by hand in the Settings VPN
// editor, since a confined app may not create the connection itself.
type SetupInfo struct {
	CACert   string `json:"ca_cert"`
	VPNUser  string `json:"vpn_user"`
	VPNPass  string `json:"vpn_pass"`
	Remote   string `json:"remote"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// runInit materialises the CA and credentials without connecting to anything,
// so the app can show the setup instructions before the first tunnel is ever
// brought up. Safe to call repeatedly: both are generated once and then reused.
func runInit(dataDir string, port int, proto string) error {
	_, dd, _, err := paths(dataDir)
	if err != nil {
		return err
	}
	p, err := EnsurePKI(dd)
	if err != nil {
		return fmt.Errorf("generating the local CA: %w", err)
	}
	user, pass, err := EnsureCreds(filepath.Join(dd, "vpn-creds"))
	if err != nil {
		return fmt.Errorf("generating local VPN credentials: %w", err)
	}
	if port == 0 {
		port, _ = strconv.Atoi(envOr("OCBRIDGE_PORT", ""))
	}
	if port == 0 {
		port = defaultPort
	}
	out, err := json.MarshalIndent(SetupInfo{
		CACert: p.CACert, VPNUser: user, VPNPass: pass,
		Remote: "127.0.0.1", Port: port, Protocol: proto,
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// resolveProto picks the transport for the local OpenVPN server. TCP is the
// default because that is what the Ubuntu Touch VPN editor writes
// (proto-tcp=yes); over loopback the choice costs nothing either way.
func resolveProto(flagValue string) string {
	v := flagValue
	if v == "" {
		v = envOr("OCBRIDGE_PROTO", "")
	}
	if v == "udp" {
		return "udp"
	}
	return "tcp"
}

// runAuthCheck verifies the local OpenVPN client's username and password.
//
// openvpn invokes this as `auth-user-pass-verify "<ocbridge> --auth" via-file`,
// passing a temporary file holding the username on line 1 and the password on
// line 2; the expected pair is in the file named by $OCBRIDGE_CREDS, which the
// generated config sets with `setenv`.
//
// This lives in the Go binary rather than in a small shell script because a
// confined click may not execute the shell. The profile transition that starts
// the app is exempt from its own exec rules -- which is why the #!/bin/sh
// launcher works -- but every exec after that is mediated, and /bin/sh
// (/usr/bin/dash) is not permitted. A shell script here would fail with a bare
// authentication failure and no hint as to why.
func runAuthCheck(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("--auth needs the credentials file openvpn passes")
	}
	want, err := readCreds(envOr("OCBRIDGE_CREDS", ""))
	if err != nil {
		return err
	}
	got, err := readCreds(args[0])
	if err != nil {
		return err
	}
	// Constant-time: the comparison is local and the secret is generated, but
	// there is no reason to be careless about it.
	userOK := subtle.ConstantTimeCompare([]byte(want[0]), []byte(got[0])) == 1
	passOK := subtle.ConstantTimeCompare([]byte(want[1]), []byte(got[1])) == 1
	if !userOK || !passOK {
		return fmt.Errorf("authentication failed")
	}
	return nil
}

func readCreds(path string) ([2]string, error) {
	var out [2]string
	if path == "" {
		return out, fmt.Errorf("no credentials file given")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	lines := splitLines(string(data))
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return out, fmt.Errorf("%s does not hold a username and a password", path)
	}
	out[0], out[1] = lines[0], lines[1]
	return out, nil
}
