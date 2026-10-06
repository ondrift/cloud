//go:build linux

package genmain

import (
	"os"
	"syscall"
)

// separateProtocolStream moves the protocol off fd 1: it duplicates the real
// stdout to a new descriptor, closed on exec so a process the handler starts
// cannot hold the slice's pipe open, then points fd 1 at stderr. os.Stdout
// and everything else writing to fd 1 reach stderr from then on.
//
// dup3 rather than dup2, which linux/arm64 does not have.
func separateProtocolStream() (*os.File, error) {
	fd, err := syscall.Dup(1)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.Dup3(2, 1, 0); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "drift-protocol"), nil
}
