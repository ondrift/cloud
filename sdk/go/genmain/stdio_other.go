//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package genmain

import (
	"errors"
	"os"
)

// separateProtocolStream cannot rearrange descriptors on this platform. A
// slice runs Linux, so this is reached only by a local experiment, which then
// shares stdout between the protocol and the handler.
func separateProtocolStream() (*os.File, error) {
	return nil, errors.New("this platform cannot move the protocol off stdout")
}
