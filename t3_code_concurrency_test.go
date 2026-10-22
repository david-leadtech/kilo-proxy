package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func t3CodeControlledValidation(t *testing.T, a *app, client string) (<-chan struct{}, func()) {
	t.Helper()
	resolve := a.launcher.resolve
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.launcher.resolve = func(id, custom string) (string, error) {
		// Capture the original route before allowing a concurrent change.
		path, err := resolve(id, custom)
		if id == client {
			once.Do(func() { close(entered); <-release })
		}
		return path, err
	}
	var closeOnce sync.Once
	return entered, func() { closeOnce.Do(func() { close(release) }) }
}

func t3CodeConcurrentRequest(a *app) <-chan error {
	finished := make(chan error, 1)
	r := httptest.NewRequest("POST", "http://localhost/t3-code/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","messages":[]}`))
	r = r.WithContext(context.WithValue(r.Context(), t3CodeRequestContextKey{}, true))
	go func() { _, err := a.prepareT3CodeRequest(r); finished <- err }()
	return finished
}

func TestT3CodeInferenceValidationDoesNotBlockState(t *testing.T) {
	a := t3CodeInferenceFixture(t)
	entered, release := t3CodeControlledValidation(t, a, "t3-code")
	finished := t3CodeConcurrentRequest(a)
	defer func() {
		release()
		if err := <-finished; err != nil {
			t.Errorf("valid request failed: %v", err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached the controlled validation block")
	}
	stateDone := make(chan int, 1)
	go func() { w := httptest.NewRecorder(); a.state(w); stateDone <- w.Code }()
	select {
	case code := <-stateDone:
		if code != 200 {
			t.Fatalf("state failed while validation was blocked: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("state is blocked by T3 validation")
	}
}

func TestT3CodeInferenceRejectsInputsChangedDuringValidation(t *testing.T) {
	for _, change := range []string{"key", "preferences", "catalog revision", "selected CLI", "automatic CLI", "profile file"} {
		t.Run(change, func(t *testing.T) {
			a := t3CodeInferenceFixture(t)
			// The original route is returned to validation after the channel
			// opens. The final route check must detect a changed auto resolver.
			baseResolve := a.launcher.resolve
			var changed atomic.Bool
			alternate := filepath.Join(a.launcher.home, "alternate-claude-native")
			if err := os.WriteFile(alternate, []byte("synthetic alternate CLI"), 0700); err != nil {
				t.Fatal(err)
			}
			a.launcher.resolve = func(id, custom string) (string, error) {
				if id == "claude" && changed.Load() {
					return alternate, nil
				}
				return baseResolve(id, custom)
			}
			entered, release := t3CodeControlledValidation(t, a, "claude")
			var forwarded atomic.Bool
			a.transport = usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
				forwarded.Store(true)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
			})
			done := make(chan *httptest.ResponseRecorder, 1)
			r := httptest.NewRequest("POST", "http://127.0.0.1:9988/t3-code/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","messages":[]}`))
			r.Header.Set("Authorization", "Bearer "+a.config.LocalKey)
			handler := a.inferenceHandler("synthetic-upstream", "synthetic-org", a.config.LocalKey, "127.0.0.1:9988")
			go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, r); done <- w }()
			defer release()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("validation did not reach controlled CLI block")
			}
			switch change {
			case "key":
				a.mu.Lock()
				a.config.LocalKey = "changed-synthetic-key"
				a.mu.Unlock()
			case "preferences":
				a.mu.Lock()
				a.config.T3Code.KiloClaude = false
				a.mu.Unlock()
			case "catalog revision":
				a.mu.Lock()
				a.catalogRevision++
				a.mu.Unlock()
			case "selected CLI":
				a.cliPathsMu.Lock()
				a.cliExecutables[1] = alternate
				a.cliPathsMu.Unlock()
			case "automatic CLI":
				changed.Store(true)
			case "profile file":
				path := filepath.Join(t3CodePaths(a.dir).Root, "profiles", "claude-kilo", "settings.json")
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			release()
			select {
			case w := <-done:
				if w.Code != 400 || forwarded.Load() {
					t.Fatalf("stale managed request forwarded: status=%d forwarded=%v body=%s", w.Code, forwarded.Load(), w.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stale validation did not finish")
			}
		})
	}
}
