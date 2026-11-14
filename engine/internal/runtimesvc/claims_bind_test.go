package runtimesvc

import (
	"net"
	"testing"
)

// The bind probe has to see a listener it did not start: in production the
// holder may belong to another user, which is exactly what the narrow table
// cannot show.
func TestCanBindPortSeesAnExistingListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if canBindPort(port) {
		_ = ln.Close()
		t.Fatalf("canBindPort(%d) = true while another listener holds it", port)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !canBindPort(port) {
		t.Fatalf("canBindPort(%d) = false after the listener closed", port)
	}
}
