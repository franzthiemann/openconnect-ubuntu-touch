package main

import (
	"fmt"
	"os"
	"syscall"
)

// PR_SET_PDEATHSIG from <linux/prctl.h>.
const prSetPDeathSig = 1

// DieWithParent asks the kernel to signal us when our parent exits.
//
// This is not belt-and-braces, it is load bearing. ocbridge's two ends are
// AF_UNIX SOCK_DGRAM sockets, and on Linux closing the far end of such a
// socketpair does NOT wake a blocked reader: no EOF, no POLLHUP, the read
// simply never returns (verified -- see tasks/lessons.md). So if openconnect is
// killed outright rather than shutting down cleanly, nothing about the socket
// would ever tell us, and a dead tunnel would keep reporting "up" in the UI
// forever.
//
// openconnect's own teardown path does kill(-pid, SIGHUP) on the script's
// process group, which we also handle; this covers the case where it never gets
// to run it.
func DieWithParent() error {
	origPPID := os.Getppid()
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetPDeathSig,
		uintptr(syscall.SIGTERM), 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_PDEATHSIG): %w", errno)
	}
	// If the parent died between the fork and the prctl above, the signal was
	// already missed and we would hang forever. Re-check.
	if os.Getppid() != origPPID {
		return fmt.Errorf("parent exited before PDEATHSIG was armed")
	}
	return nil
}
