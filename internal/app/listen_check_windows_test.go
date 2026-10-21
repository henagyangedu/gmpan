//go:build windows

package app

import (
	"net"
	"testing"
)

func TestWindowsListenerConflictDetection(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "127.0.0.1"} {
		listener, err := net.Listen("tcp4", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := checkIPv4Listeners(net.ParseIP("127.0.0.1"), uint16(port)); err == nil {
			listener.Close()
			t.Fatalf("existing %s listener was missed", host)
		}
		listener.Close()
		if err := checkIPv4Listeners(net.ParseIP("127.0.0.1"), uint16(port)); err != nil {
			t.Fatal("closed listener was still reported as occupied")
		}
	}
}
