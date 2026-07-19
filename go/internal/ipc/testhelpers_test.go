package ipc

import (
	"net"
	"testing"
)

// pipe returns two ends of an in-memory full-duplex connection suitable
// for testing the length-prefixed codec without spinning up a real OS pipe.
//
// We use net.Pipe (not io.Pipe) because net.Pipe gives a single Read+Write
// interface on each end, so WriteFrame and ReadFrame can share one value.
// io.Pipe is unidirectional — splicing two of them into a "duplex" deadlocks
// when one goroutine tries to write on end A while no one is reading end B.
func pipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	return a, b
}
