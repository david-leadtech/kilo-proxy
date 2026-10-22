package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type remoteImageTestRoundTripper func(*http.Request) (*http.Response, error)

func (f remoteImageTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type remoteImageTestBody struct {
	reader        io.Reader
	reads, closes int
}

func (b *remoteImageTestBody) Read(p []byte) (int, error) { b.reads++; return b.reader.Read(p) }
func (b *remoteImageTestBody) Close() error               { b.closes++; return nil }

func remoteImagePlannedRequest(ctx context.Context) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://gateway.example/v1/responses", strings.NewReader("original"))
	return r.WithContext(context.WithValue(ctx, remoteImageFallbackContextKey{}, &remoteImageFallbackPlan{body: []byte("sanitized"), skipped: 1}))
}

func TestRemoteImageFallbackLeavesStreamingAndNonCandidateResponsesUnread(t *testing.T) {
	for _, status := range []int{200, 201, 204, 302, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			pipe, writer := io.Pipe()
			defer pipe.Close()
			defer writer.Close()
			body := &remoteImageTestBody{reader: pipe}
			original := &http.Response{StatusCode: status, Body: body, Header: http.Header{"Content-Type": {"text/event-stream"}}}
			calls := 0
			transport := remoteImageFallbackTransport{base: remoteImageTestRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return original, nil })}
			done := make(chan struct{})
			var got *http.Response
			var err error
			go func() { got, err = transport.RoundTrip(remoteImagePlannedRequest(context.Background())); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				writer.Close()
				<-done
				t.Fatal("transport buffered a non-candidate streaming response")
			}
			if got != original || err != nil || calls != 1 || body.reads != 0 || body.closes != 0 {
				t.Fatalf("response=%p err=%v calls=%d reads=%d closes=%d", got, err, calls, body.reads, body.closes)
			}
		})
	}
}

type remoteImageOneShotReadError struct {
	body []byte
	err  error
	sent bool
}

func (r *remoteImageOneShotReadError) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, r.body), r.err
}

func TestRemoteImageFallbackPreservesResponseReadErrorAndClose(t *testing.T) {
	sentinel := errors.New("upstream body interrupted")
	body := &remoteImageTestBody{reader: &remoteImageOneShotReadError{body: []byte("partial"), err: sentinel}}
	_, complete, replay := readReplayableResponseBody(body, remoteImageFallbackResponseLimit)
	if complete {
		t.Fatal("partial read was reported complete")
	}
	got, err := io.ReadAll(replay)
	if string(got) != "partial" || !errors.Is(err, sentinel) {
		t.Fatalf("replayed body=%q err=%v, want original read error", got, err)
	}
	replay.Close()
	if body.closes != 1 {
		t.Fatalf("close calls=%d", body.closes)
	}
}

func TestRemoteImageFallbackPreservesOversizeResponseAndClose(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), remoteImageFallbackResponseLimit+13)
	body := &remoteImageTestBody{reader: bytes.NewReader(payload)}
	_, complete, replay := readReplayableResponseBody(body, remoteImageFallbackResponseLimit)
	if complete || body.closes != 0 {
		t.Fatal("oversize response consumed ownership")
	}
	got, err := io.ReadAll(replay)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("oversize replay changed: %d %v", len(got), err)
	}
	replay.Close()
	if body.closes != 1 {
		t.Fatalf("close calls=%d", body.closes)
	}
}

const remoteImageKnownError = `{"error":{"message":"Error while downloading file."}}`

func TestRemoteImageFallbackClosesOriginalResponseExactlyOnce(t *testing.T) {
	original := &remoteImageTestBody{reader: strings.NewReader(remoteImageKnownError)}
	calls := 0
	transport := remoteImageFallbackTransport{base: remoteImageTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		r.Body.Close()
		if calls == 1 {
			return &http.Response{StatusCode: 400, Body: original}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	response, err := transport.RoundTrip(remoteImagePlannedRequest(context.Background()))
	if err != nil || response.StatusCode != 200 || calls != 2 {
		t.Fatalf("response=%v err=%v calls=%d", response, err, calls)
	}
	response.Body.Close()
	if original.closes != 1 {
		t.Fatalf("original body closed %d times, want exactly once", original.closes)
	}
}

func TestRemoteImageFallbackPropagatesRetryCancellation(t *testing.T) {
	for _, beforeRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-retry", true: "before-retry"}[beforeRetry], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			transport := remoteImageFallbackTransport{base: remoteImageTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				r.Body.Close()
				if calls == 1 {
					if beforeRetry {
						cancel()
					}
					return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(remoteImageKnownError))}, nil
				}
				cancel()
				return nil, ctx.Err()
			})}
			response, err := transport.RoundTrip(remoteImagePlannedRequest(ctx))
			if response != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("response=%v err=%v, cancellation was hidden", response, err)
			}
			wanted := 2
			if beforeRetry {
				wanted = 1
			}
			if calls != wanted {
				t.Fatalf("calls=%d want%d", calls, wanted)
			}
		})
	}
}

func TestRemoteImageFallbackPreservesOriginalOnOrdinaryRetryTransportFailure(t *testing.T) {
	calls := 0
	transport := remoteImageFallbackTransport{base: remoteImageTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		r.Body.Close()
		if calls == 1 {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(remoteImageKnownError))}, nil
		}
		return nil, errors.New("connection refused")
	})}
	response, err := transport.RoundTrip(remoteImagePlannedRequest(context.Background()))
	if err != nil || response == nil || response.StatusCode != 400 || calls != 2 {
		t.Fatalf("response=%v err=%v calls=%d", response, err, calls)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || string(body) != remoteImageKnownError {
		t.Fatalf("original error changed: %q %v", body, readErr)
	}
}

func TestRemoteImageFallbackMatchesOnlyKnownProviderDownloadError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"known", 400, remoteImageKnownError, true},
		{"known-normalized", 400, `{"error":{"message":" Error WHILE downloading file. "}}`, true},
		{"metadata", 400, `{"error":{"message":"invalid request"},"help":"download file"}`, false},
		{"quota", 400, `{"error":{"message":"image download file quota exceeded"}}`, false},
		{"html", 400, "<html>Cannot download image file</html>", false},
		{"permission", 403, remoteImageKnownError, false},
		{"rate-limit", 429, remoteImageKnownError, false},
		{"upstream", 500, remoteImageKnownError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRemoteImageDownloadFailure(&http.Response{StatusCode: tc.status}, []byte(tc.body)); got != tc.want {
				t.Fatalf("matched=%v want%v", got, tc.want)
			}
		})
	}
}

func TestRemoteImageFallbackOnlyRecognizesAbsoluteHTTPImageURLs(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, address := range []string{"https://images.example/a.png", "http://images.example/a.png", "file:///tmp/a.png", "/tmp/a.png", "s3://bucket/a.png", "data:image/png;base64,YQ==", "https:/missing-host", "//images.example/a.png"} {
			part := map[string]any{"type": "input_image", "image_url": address}
			if path == "/v1/chat/completions" {
				part = map[string]any{"type": "image_url", "image_url": map[string]any{"url": address}}
			}
			if path == "/v1/messages" {
				part = map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": address}}
			}
			want := strings.HasPrefix(address, "https://") || strings.HasPrefix(address, "http://")
			t.Run(path+"/"+address, func(t *testing.T) {
				if _, got := remoteImageURL(part, path); got != want {
					t.Fatalf("remote=%v want%v", got, want)
				}
			})
		}
	}
}

func TestRemoteImageFallbackRecordsFinalJSONUsage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(400)
			io.WriteString(w, remoteImageKnownError)
			return
		}
		io.WriteString(w, `{"output":[],"usage":{"input_tokens":7,"output_tokens":3,"cost":0.000025}}`)
	}))
	defer server.Close()
	a := captureTestApp(t)
	setUpstream(a, server.URL)
	response := activityRequest(a, `{"model":"test/model","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://stale.example/a.png"}]}]}`)
	if response.Code != 200 || calls.Load() != 2 || len(a.events) != 1 {
		t.Fatalf("status=%d calls=%d events=%d", response.Code, calls.Load(), len(a.events))
	}
	usage := a.events[0].Usage
	if usage == nil || usage.Input == nil || *usage.Input != 7 || usage.Output == nil || *usage.Output != 3 || !usage.Complete ||
		usage.CostUSD == nil || *usage.CostUSD != "0.000025000" {
		t.Fatalf("final JSON usage was lost: %+v", usage)
	}
	if a.usageTotal.Requests != 1 || a.usageTotal.Priced != 1 || a.usageTotal.Input != 7 || a.usageTotal.Output != 3 {
		t.Fatalf("recovery usage was counted incorrectly: %+v", a.usageTotal)
	}
}

func TestRemoteImageFallbackPreservesToolAndFileContext(t *testing.T) {
	cases := []struct{ path, payload, remote, marker string }{
		{"/v1/responses",
			`{"model":"test/model","input":[{"role":"user","content":[REMOTE,{"type":"input_image","image_url":"data:image/png;base64,YQ=="},{"type":"input_image","file_id":"image_1"},{"type":"input_file","file_url":"https://files.example/a.pdf"}]},{"type":"function_call","call_id":"call_1","arguments":"{\"url\":\"https://stale.example/a.png\"}"},{"type":"function_call_output","call_id":"call_1","output":[REMOTE,{"type":"input_text","text":"https://stale.example/a.png"},{"type":"input_file","file_id":"file_2"}]}],"metadata":{"integer":9007199254740993}}`,
			`{"type":"input_image","image_url":"https://stale.example/a.png","detail":"high"}`,
			`{"type":"input_text","text":"[Kilo Proxy: unavailable image omitted]"}`},
		{"/v1/chat/completions",
			`{"model":"test/model","messages":[{"role":"user","content":[REMOTE,{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}},{"type":"file","file":{"file_id":"file_1"}}]},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"fetch","arguments":"{\"url\":\"https://stale.example/a.png\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"https://stale.example/a.png"}]}`,
			`{"type":"image_url","image_url":{"url":"https://stale.example/a.png","detail":"high"}}`,
			`{"type":"text","text":"[Kilo Proxy: unavailable image omitted]"}`},
		{"/v1/messages",
			`{"model":"test/model","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","is_error":false,"cache_control":{"type":"ephemeral"},"content":[REMOTE,{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}},{"type":"document","source":{"type":"url","url":"https://files.example/a.pdf"}},{"type":"text","text":"https://stale.example/a.png"}]}]}]}`,
			`{"type":"image","source":{"type":"url","url":"https://stale.example/a.png"}}`,
			`{"type":"text","text":"[Kilo Proxy: unavailable image omitted]"}`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			payload := strings.ReplaceAll(tc.payload, "REMOTE", tc.remote)
			expected := strings.ReplaceAll(tc.payload, "REMOTE", tc.marker)
			request := httptest.NewRequest("POST", "http://127.0.0.1"+tc.path, strings.NewReader(payload))
			prepared, err := prepareRemoteImageFallback(request)
			if err != nil {
				t.Fatal(err)
			}
			plan, _ := prepared.Context().Value(remoteImageFallbackContextKey{}).(*remoteImageFallbackPlan)
			if plan == nil {
				t.Fatal("missing fallback plan")
			}
			got, err := decodeObject(plan.body)
			if err != nil {
				t.Fatal(err)
			}
			want, err := decodeObject([]byte(expected))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("non-image context changed\n got:%s\nwant:%s", plan.body, expected)
			}
			original, err := io.ReadAll(prepared.Body)
			prepared.Body.Close()
			if err != nil || string(original) != payload {
				t.Fatalf("first request changed: %q %v", original, err)
			}
		})
	}
}
