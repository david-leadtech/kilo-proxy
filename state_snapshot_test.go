package main

import (
	"net/http"
	"testing"
	"time"
)

type heldStateWriter struct {
	entered, release chan struct{}
	header           http.Header
}

func (w *heldStateWriter) Header() http.Header { return w.header }
func (w *heldStateWriter) WriteHeader(int)     {}
func (w *heldStateWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

func TestStateResponseWriteDoesNotHoldProxyMutex(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	a.imageDependencyLookup = func(string) string { return "" }
	w := &heldStateWriter{make(chan struct{}), make(chan struct{}), make(http.Header)}
	finished := make(chan struct{})
	go func() { a.state(w); close(finished) }()
	<-w.entered
	acquired := make(chan struct{})
	go func() { a.mu.Lock(); a.mu.Unlock(); close(acquired) }()
	blocked := false
	select {
	case <-acquired:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	close(w.release)
	<-finished
	<-acquired
	if blocked {
		t.Fatal("a slow state response writer held the proxy mutex")
	}
}
