//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package genmain

import (
	"os"
	"syscall"
)

// separateProtocolStream moves the protocol off fd 1, as on Linux: the real
// stdout is duplicated to a new descriptor, closed on exec, and fd 1 is
// pointed at stderr.
func separateProtocolStream() (*os.File, error) {
	fd, err := syscall.Dup(1)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.Dup2(2, 1); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "drift-protocol"), nil
}
