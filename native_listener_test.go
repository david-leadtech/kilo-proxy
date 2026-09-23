//go:build desktop

package main

import (
	"net"
	"sync"
	"testing"
)

// Reserve the fixture's advertised port until the real proxy takes ownership.
// Closing a probe socket before building HTTP fixtures lets their listeners or
// outgoing requests steal the same ephemeral port, especially on Linux CI.
func nativeReserveProxyPort(t *testing.T, a *app) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.config.Port = listener.Addr().(*net.TCPAddr).Port
	var mu sync.Mutex
	reserved := listener
	a.listenProxy = func(network, address string) (net.Listener, error) {
		mu.Lock()
		defer mu.Unlock()
		if reserved != nil && network == "tcp4" && address == reserved.Addr().String() {
			owned := reserved
			reserved = nil
			return owned, nil
		}
		// Changed ports and later starts must still bind a real socket, keeping
		// occupied-port checks and production start/stop behavior intact.
		return net.Listen(network, address)
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if reserved != nil {
			_ = reserved.Close()
		}
	})
	return listener
}

func TestNativeProxyReservationTransfersOriginalSocketToRealStartup(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "fixture-key", "fixture-team"
	reserved := nativeReserveProxyPort(t, a)
	if other, err := net.Listen("tcp4", reserved.Addr().String()); err == nil {
		other.Close()
		t.Fatal("fixture exposed its configured port before proxy startup")
	}
	if a.proxyServer != nil || a.proxyListener != nil {
		t.Fatal("reserving a port incorrectly reported a running proxy")
	}
	if err := a.start(); err != nil {
		t.Fatal(err)
	}
	if a.proxyListener != reserved {
		t.Fatal("startup closed and rebound the fixture socket instead of taking ownership")
	}
	assertLaunchProxyListening(t, a)
	if err := a.start(); err != nil || a.proxyListener != reserved {
		t.Fatalf("repeated start did not preserve the running listener: %v", err)
	}
}

func TestNativeProxyReservationPreservesOccupiedPortFailure(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "fixture-key", "fixture-team"
	reserved := nativeReserveProxyPort(t, a)
	reservedPort := a.config.Port
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	a.config.Port = occupied.Addr().(*net.TCPAddr).Port
	if err := a.start(); err == nil || a.proxyServer != nil || a.proxyListener != nil {
		t.Fatalf("fixture hid a real occupied-port startup failure: %v", err)
	}
	a.config.Port = reservedPort
	if err := a.start(); err != nil || a.proxyListener != reserved {
		t.Fatalf("failed startup consumed the unrelated reserved socket: %v", err)
	}
}
