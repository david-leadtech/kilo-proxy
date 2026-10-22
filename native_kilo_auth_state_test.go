//go:build desktop

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNativeKiloAuthCancelIgnoresOlderPendingState(t *testing.T) {
	u := nativeFreshOnboarding(t)
	authServer(t, u.owner, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/device-auth/codes":
			beginResponse(w)
		case "/api/device-auth/codes/TEST-CODE":
			jsonResponse(w, 202, map[string]string{"status": "pending"})
		default:
			http.NotFound(w, r)
		}
	})
	u.beginKiloLogin()
	nativeTestWait(t, u, func() bool {
		return !u.busy["POST/api/auth/start"] && !u.busy["GET/api/state"] && nativeString(nativeMap(u.state["auth"]), "status") == "pending"
	})
	release := nativeOnboardingHoldStateSnapshot(t, u)
	u.call("POST", "/api/auth/cancel", map[string]any{}, u.acceptState)
	nativeTestWait(t, u, func() bool {
		return !u.busy["POST/api/auth/cancel"] && nativeString(nativeMap(u.state["auth"]), "status") == "cancelled"
	})
	u.owner.mu.Lock()
	backendBefore := u.owner.login.Status
	u.owner.mu.Unlock()
	if backendBefore != "cancelled" {
		t.Fatalf("backend before delayed GET=%s", backendBefore)
	}
	release()
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	u.owner.mu.Lock()
	backendAfter := u.owner.login.Status
	u.owner.mu.Unlock()
	uiAfter := nativeString(nativeMap(u.state["auth"]), "status")
	t.Logf("held first-login GET crossed completed cancel: UI=%s backend=%s", uiAfter, backendAfter)
	if backendAfter != "cancelled" {
		t.Fatalf("backend cancellation changed: %s", backendAfter)
	}
	if uiAfter != "cancelled" {
		t.Fatalf("older pending snapshot erased completed cancellation: UI=%s backend=%s", uiAfter, backendAfter)
	}
}

func nativeKiloAuthPendingFixture(t *testing.T) (*nativeUI, *atomic.Bool) {
	t.Helper()
	u := nativeFreshOnboarding(t)
	approve := new(atomic.Bool)
	authServer(t, u.owner, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/device-auth/codes":
			beginResponse(w)
		case "/api/device-auth/codes/TEST-CODE":
			if approve.Load() {
				jsonResponse(w, 200, map[string]string{"status": "approved", "token": "synthetic-fresh-device-key", "userEmail": "developer@example.test"})
			} else {
				jsonResponse(w, 202, map[string]string{"status": "pending"})
			}
		case "/api/profile":
			jsonResponse(w, 200, map[string]any{"email": "developer@example.test", "organizations": []organization{{ID: "e2e-team", Name: "Engineering"}}})
		default:
			http.NotFound(w, r)
		}
	})
	u.beginKiloLogin()
	nativeTestWait(t, u, func() bool {
		return !u.busy["POST/api/auth/start"] && !u.busy["GET/api/state"] && nativeString(nativeMap(u.state["auth"]), "status") == "pending"
	})
	return u, approve
}

func TestNativeKiloAuthStartIgnoresOlderCancelledState(t *testing.T) {
	u, approve := nativeKiloAuthPendingFixture(t)
	u.call("POST", "/api/auth/cancel", map[string]any{}, u.acceptState)
	nativeTestWait(t, u, func() bool {
		return !u.busy["POST/api/auth/cancel"] && nativeString(nativeMap(u.state["auth"]), "status") == "cancelled"
	})
	release := nativeOnboardingHoldStateSnapshot(t, u)
	u.beginKiloLogin()
	nativeTestWait(t, u, func() bool {
		return !u.busy["POST/api/auth/start"] && nativeString(nativeMap(u.state["auth"]), "status") == "pending"
	})
	release()
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	if got := nativeString(nativeMap(u.state["auth"]), "status"); got != "pending" {
		t.Fatalf("older cancellation erased new pending login: %s", got)
	}
	approve.Store(true)
	waitLogin(t, u.owner, "approved")
	u.refreshState()
	nativeTestWait(t, u, func() bool {
		return !u.busy["GET/api/state"] && nativeString(nativeMap(u.state["auth"]), "status") == "approved"
	})
	if !u.connectionHasKey() {
		t.Fatal("fresh approved login did not publish its credential")
	}
}

// Hold only the response of a completed authenticated mutation. The GET then
// captures real backend state while the native mutation callback is still busy.
func nativeKiloAuthHoldMutationResponse(t *testing.T, u *nativeUI, path string, action func()) func() {
	t.Helper()
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	api := u.owner.adminHandler()
	started, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == path {
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			close(started)
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			for key, values := range response.Header() {
				w.Header()[key] = append([]string(nil), values...)
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { release(); server.Close() })
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	action()
	nativeTestWait(t, u, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})
	return release
}

func TestNativeKiloAuthStateWhileMutationBusy(t *testing.T) {
	for _, path := range []string{"/api/auth/start", "/api/auth/cancel"} {
		t.Run(path, func(t *testing.T) {
			u, _ := nativeKiloAuthPendingFixture(t)
			before, after := "pending", "cancelled"
			action := func() { u.call("POST", path, map[string]any{}, u.acceptState) }
			if path == "/api/auth/start" {
				u.call("POST", "/api/auth/cancel", map[string]any{}, u.acceptState)
				nativeTestWait(t, u, func() bool {
					return !u.busy["POST/api/auth/cancel"] && nativeString(nativeMap(u.state["auth"]), "status") == "cancelled"
				})
				before, after = "cancelled", "pending"
				action = u.beginKiloLogin
			}
			releaseMutation := nativeKiloAuthHoldMutationResponse(t, u, path, action)
			releaseState := nativeOnboardingHoldStateSnapshot(t, u)
			releaseState()
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
			if !u.busy["POST"+path] {
				t.Fatal("mutation response was not held")
			}
			if got := nativeString(nativeMap(u.state["auth"]), "status"); got != before {
				t.Fatalf("poll crossed in-flight mutation: got %s want prior %s", got, before)
			}
			releaseMutation()
			nativeTestWait(t, u, func() bool {
				return !u.busy["POST"+path] && nativeString(nativeMap(u.state["auth"]), "status") == after
			})
		})
	}
}
