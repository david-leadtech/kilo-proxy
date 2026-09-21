package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureTestApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	if response := adminRequest(a, "activity/config", `{"enabled":true}`); response.Code != http.StatusOK {
		t.Fatalf("enable capture: %d %s", response.Code, response.Body.String())
	}
	return a
}

func TestActivityIsOptInAndChoiceSurvivesRestart(t *testing.T) {
	a := testApp(t)
	if a.captureEnabled || a.config.CaptureActivity {
		t.Fatal("new profile enabled request capture")
	}
	// Settings created by older versions had no capture preference. Upgrading
	// must not silently opt the user into retaining messages.
	legacy, _ := json.Marshal(a.config)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(legacy, &fields)
	delete(fields, "captureActivity")
	legacy, _ = json.Marshal(fields)
	if err := os.WriteFile(filepath.Join(a.dir, "settings.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	checkRestart := func(want bool) {
		t.Helper()
		restarted, err := newApp(a.dir, &fakeVault{values: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restarted.stop)
		if restarted.captureEnabled != want || restarted.config.CaptureActivity != want {
			t.Fatalf("capture preference after restart = %v, want %v", restarted.captureEnabled, want)
		}
		if len(restarted.events) != 0 || len(restarted.traces) != 0 {
			t.Fatal("restarted profile restored request history")
		}
	}
	checkRestart(false)
	for _, want := range []bool{true, false} {
		body, _ := json.Marshal(map[string]bool{"enabled": want})
		if response := adminRequest(a, "activity/config", string(body)); response.Code != http.StatusOK {
			t.Fatal(response.Code)
		}
		checkRestart(want)
	}
}

func TestActivityDisabledRetainsNoRequestsButKeepsAccounting(t *testing.T) {
	a := testApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"output":[],"usage":{"input_tokens":10,"output_tokens":2,"cost":"0.000123456"}}`)
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	if w := activityRequest(a, `{"model":"test/model","input":"private prompt"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(a.events) != 0 || len(a.traces) != 0 || len(a.activeCaptures) != 0 || a.activeTraces != 0 {
		t.Fatal("disabled capture retained a request")
	}
	if a.requests != 1 || a.usageTotal.Requests != 1 || a.usageTotal.CostUSD != "0.000123456" || a.usageTotal.Input != 10 {
		t.Fatalf("accounting stopped with capture: %+v", a.usageTotal)
	}
	if response := adminRequest(a, "activity/1", ""); response.Code != http.StatusNotFound {
		t.Fatal("disabled request accessible through inspector")
	}
}

func TestActivityDisabledImageGenerationStillRecordsCost(t *testing.T) {
	data := imageTestPNG(t)
	a := imageTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, imageTestResponse(data, map[string]any{"cost": "0.04", "prompt_tokens": 10, "completion_tokens": 2}))
	})
	if response := adminRequest(a, "activity/config", `{"enabled":false}`); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if _, err := imageTestGenerate(a, imageGenerationArguments{Prompt: "private image prompt"}); err != nil {
		t.Fatal(err)
	}
	if len(a.events) != 0 || len(a.traces) != 0 || a.activeTraces != 0 || len(a.activeCaptures) != 0 {
		t.Fatal("image generation retained a capture while disabled")
	}
	if a.usageTotal.CostUSD != "0.040000000" || a.usageTotal.Requests != 1 {
		t.Fatalf("image generation cost missing with capture off: %+v", a.usageTotal)
	}
}

func TestActivityDisablePurgesInFlightCopiesAndPreventsResurrection(t *testing.T) {
	for _, image := range []bool{false, true} {
		name := "responses"
		if image {
			name = "images"
		}
		t.Run(name, func(t *testing.T) {
			a := captureTestApp(t)
			r := httptest.NewRequest("POST", "http://localhost/v1/responses", nil)
			r.Header.Set("X-Private-Debug", "secret headers")
			id, epoch, capture := a.beginActivity(r, "key", "local")
			capture.request.write([]byte("private request"))
			capture.upstreamRequest(r, false)
			capture.upRequest.write([]byte("private upstream request"))
			capture.upstreamResponse(&http.Response{Header: http.Header{"X-Private-Debug": []string{"secret response"}}, StatusCode: 200}, false)
			capture.upResponse.write([]byte("private upstream response"))
			capture.response.write([]byte("private response"))
			a.events = []event{{ID: "completed", HasDetails: true}}
			a.traces["completed"] = capture.finish("completed", nil)
			if response := adminRequest(a, "activity/config", `{"enabled":false}`); response.Code != 200 {
				t.Fatal(response.Code)
			}
			if len(a.events) != 0 || len(a.traces) != 0 || len(capture.requestHeaders) != 0 || len(capture.upRequestHeaders) != 0 || len(capture.upResponseHeaders) != 0 {
				t.Fatal("disabling did not discard retained messages and headers")
			}
			for _, b := range []*traceBuffer{&capture.request, &capture.upRequest, &capture.upResponse, &capture.response} {
				b.write([]byte("late stream data"))
				if len(b.data) != 0 || b.total != 0 {
					t.Fatal("in-flight buffer retained content after disabling")
				}
			}
			if response := adminRequest(a, "activity/config", `{"enabled":true}`); response.Code != 200 {
				t.Fatal(response.Code)
			}
			capture.upstreamRequest(r, false)
			capture.upstreamResponse(&http.Response{Header: http.Header{"X-Late": []string{"secret"}}}, false)
			capture.setError("late secret error")
			if capture.finish(id, nil) != nil || epoch == a.activityEpoch || capture.traceError != "" || len(capture.upRequestHeaders) != 0 || len(capture.upResponseHeaders) != 0 {
				t.Fatal("reenabling resurrected an earlier capture")
			}
			if image {
				activity := &imageGenerationActivity{owner: a, id: id, epoch: epoch, capture: capture, usage: newUsageObserver(r, "team"), status: 200}
				activity.finish(&imageGenerationResult{}, nil)
				if len(a.events) != 0 || len(a.traces) != 0 || a.activeTraces != 0 || len(a.activeCaptures) != 0 || a.usageTotal.Requests != 1 {
					t.Fatal("image completion restored disabled captures or lost usage")
				}
			} else {
				// This branch owns a synthetic in-flight capture without an HTTP
				// handler. Release its counters as a handler's defer would.
				a.mu.Lock()
				a.active--
				a.activeTraces--
				delete(a.activeCaptures, capture)
				a.mu.Unlock()
			}
		})
	}
}

func TestActivityDisableDuringRequestPreservesStreamAndUsage(t *testing.T) {
	for _, initiallyEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "disable and reenable", false: "enable during request"}[initiallyEnabled], func(t *testing.T) {
			a := testApp(t)
			if initiallyEnabled {
				adminRequest(a, "activity/config", `{"enabled":true}`)
			}
			started, release := make(chan struct{}), make(chan struct{})
			payload := `{"output":[],"usage":{"input_tokens":10,"output_tokens":2,"cost":"0.01"}}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				<-release
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, payload)
			}))
			defer upstream.Close()
			setUpstream(a, upstream.URL)
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { finished <- activityRequest(a, `{"model":"test/model","input":"private"}`) }()
			<-started
			if initiallyEnabled {
				adminRequest(a, "activity/config", `{"enabled":false}`)
			}
			adminRequest(a, "activity/config", `{"enabled":true}`)
			close(release)
			response := <-finished
			if response.Code != 200 || response.Body.String() != payload {
				t.Fatal("capture choice changed forwarded traffic")
			}
			if len(a.events) != 0 || len(a.traces) != 0 || len(a.activeCaptures) != 0 || a.activeTraces != 0 || a.usageTotal.CostUSD != "0.010000000" {
				t.Fatal("earlier request was captured or lost its cost")
			}
		})
	}
}

func TestActivityPreferenceFailureDoesNotChangeCapture(t *testing.T) {
	a := captureTestApp(t)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"yes"}`} {
		if response := adminRequest(a, "activity/config", body); response.Code != 400 || !a.captureEnabled {
			t.Fatal("invalid preference changed capture")
		}
	}
	path := filepath.Join(a.dir, "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	response := adminRequest(a, "activity/config", `{"enabled":false}`)
	if response.Code != 500 || !a.captureEnabled || !a.config.CaptureActivity || !strings.Contains(response.Body.String(), "Could not save") {
		t.Fatal("failed persistence silently changed capture")
	}
}
