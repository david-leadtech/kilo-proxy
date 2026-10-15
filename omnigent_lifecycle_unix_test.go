//go:build darwin || linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOmnigentStopWaitsForOwnedCleanup(t *testing.T) {
	a := testApp(t)
	root := omnigentPaths(a.dir).Root
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := openProfileLock(filepath.Join(root, "runtime.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockProfileFile(lock); err != nil {
		t.Fatal(err)
	}
	defer unlockProfileFile(lock)
	token := strings.Repeat("s", 40)
	acknowledged, cleaned := make(chan struct{}), make(chan struct{})
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stop" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "invalid control request", 403)
			return
		}
		w.WriteHeader(200)
		close(acknowledged)
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = os.MkdirAll(filepath.Join(root, "runtime"), 0700)
			_ = atomicCatalogFile(filepath.Join(root, "runtime", "status.json"), []byte(`{"version":1,"fingerprint":"synthetic-owned-runtime","phase":"stopped"}`))
			_ = os.Remove(filepath.Join(root, "runtime.json"))
			unlockProfileFile(lock)
			close(cleaned)
		}()
	}))
	defer control.Close()
	parsed, _ := url.Parse(control.URL)
	port, _ := strconv.Atoi(parsed.Port())
	record := omnigentRuntimeRecord{Version: 1, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ServerPort: port, ControlPort: port, ControlToken: token, Fingerprint: "synthetic-owned-runtime"}
	raw, _ := json.Marshal(record)
	if err := atomicCatalogFile(filepath.Join(root, "runtime.json"), raw); err != nil {
		t.Fatal(err)
	}
	a.omnigentLaunchUntil = time.Now().Add(time.Minute)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- adminRequest(a, "clients/omnigent/stop", `{}`) }()
	<-acknowledged
	select {
	case <-result:
		t.Fatal("stop returned before its owned runtime cleaned up")
	default:
	}
	<-cleaned
	w := <-result
	if w.Code != 200 {
		t.Fatalf("stop status %d: %s", w.Code, w.Body.String())
	}
	if err := a.omnigentIdle(); err != nil {
		t.Fatalf("stopped workspace was not immediately editable: %v", err)
	}
}

func TestOmnigentStopRejectsFailedCleanupReceipt(t *testing.T) {
	a := testApp(t)
	root := omnigentPaths(a.dir).Root
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 40)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stop" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "invalid control request", 403)
			return
		}
		if err := atomicCatalogFile(filepath.Join(root, "runtime", "status.json"), []byte(`{"version":1,"fingerprint":"synthetic-owned-runtime","phase":"failed","message":"private diagnostic must not be forwarded"}`)); err != nil {
			t.Error(err)
		}
		if err := os.Remove(filepath.Join(root, "runtime.json")); err != nil {
			t.Error(err)
		}
		w.WriteHeader(200)
	}))
	defer control.Close()
	parsed, _ := url.Parse(control.URL)
	port, _ := strconv.Atoi(parsed.Port())
	record := omnigentRuntimeRecord{Version: 1, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ServerPort: port, ControlPort: port, ControlToken: token, Fingerprint: "synthetic-owned-runtime"}
	raw, _ := json.Marshal(record)
	if err := atomicCatalogFile(filepath.Join(root, "runtime.json"), raw); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"acknowledged-but-failed", "already-inactive-with-failed-receipt"} {
		t.Run(stage, func(t *testing.T) {
			w := adminRequest(a, "clients/omnigent/stop", `{}`)
			if w.Code != 409 || strings.Contains(w.Body.String(), "private diagnostic") || !strings.Contains(w.Body.String(), "did not finish stopping") {
				t.Fatalf("failed cleanup reported an incorrect stop result: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOmnigentStopRefusesUncontrolledStartup(t *testing.T) {
	a := testApp(t)
	root := omnigentPaths(a.dir).Root
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := openProfileLock(filepath.Join(root, "runtime.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockProfileFile(lock); err != nil {
		t.Fatal(err)
	}
	defer unlockProfileFile(lock)
	if w := adminRequest(a, "clients/omnigent/stop", `{}`); w.Code != 409 {
		t.Fatalf("uncontrolled startup reported a successful stop: %d", w.Code)
	}
	unlockProfileFile(lock)
	a.omnigentLaunchUntil = time.Now().Add(time.Minute)
	if w := adminRequest(a, "clients/omnigent/stop", `{}`); w.Code != 409 {
		t.Fatalf("pending startup reported a successful stop: %d", w.Code)
	}
	a.omnigentLaunchUntil = time.Time{}
	if w := adminRequest(a, "clients/omnigent/stop", `{}`); w.Code != 200 {
		t.Fatalf("idle stop status %d: %s", w.Code, w.Body.String())
	}
}
