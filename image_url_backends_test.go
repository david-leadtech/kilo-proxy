package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type imageURLTestTransport func(*http.Request) (*http.Response, error)

func (f imageURLTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type imageURLTestProcess struct {
	origin string
	done   chan struct{}
	once   sync.Once
	closes atomic.Int32
}

func (p *imageURLTestProcess) URL() string           { return p.origin }
func (p *imageURLTestProcess) Done() <-chan struct{} { return p.done }
func (p *imageURLTestProcess) Close() error {
	p.once.Do(func() { p.closes.Add(1); close(p.done) })
	return nil
}
func imageURLTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.NRGBA{R: 71, G: 132, B: 219, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func imageURLTestManager(t *testing.T) (*imageURLBackendManager, *atomic.Int32) {
	t.Helper()
	starts := &atomic.Int32{}
	m := &imageURLBackendManager{deps: &imageURLBackendDeps{start: func(_ context.Context, _ string, origin string) (imageURLTunnelProcess, error) {
		starts.Add(1)
		return &imageURLTestProcess{origin: origin, done: make(chan struct{})}, nil
	}}}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, starts
}
func imageURLTestGet(t *testing.T, method, target string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := imageURLHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw, resp.Header
}

func TestImageURLTunnelLeaseIsolationAndSurface(t *testing.T) {
	m, starts := imageURLTestManager(t)
	first, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatal("tunnel was not reused")
	}
	if time.Until(first.ExpiresAt()) <= imageUploadRequestTimeout {
		t.Fatal("lease shorter than inference timeout")
	}
	raw := imageURLTestPNG(t)
	firstURL, err := first.Upload(context.Background(), raw, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	secondURL, err := second.Upload(context.Background(), raw, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if firstURL == secondURL {
		t.Fatal("leases share a bearer URL")
	}
	parsed, _ := url.Parse(firstURL)
	if len(parsed.Path) != 65 {
		t.Fatal("image token does not carry 256 bits")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		status, got, headers := imageURLTestGet(t, method, firstURL)
		if status != 200 || headers.Get("Content-Type") != "image/png" || headers.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(headers.Get("Cache-Control"), "no-store") {
			t.Fatalf("unsafe response: %d %v", status, headers)
		}
		if method == http.MethodGet && !bytes.Equal(got, raw) {
			t.Fatal("original image bytes changed")
		}
		if method == http.MethodHead && len(got) != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	for _, path := range []string{"/", "/api/status", "/metrics", "/etc/passwd", "/../api/status", parsed.Path + "?x=1", "/%" + "61" + parsed.Path[2:]} {
		status, _, _ := imageURLTestGet(t, http.MethodGet, parsed.Scheme+"://"+parsed.Host+path)
		if status != 404 {
			t.Fatalf("unexpected surface %s: %d", path, status)
		}
	}
	status, _, _ := imageURLTestGet(t, http.MethodPost, firstURL)
	if status != 405 {
		t.Fatal("non-read method accepted")
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _, _ = imageURLTestGet(t, http.MethodGet, firstURL)
	if status != 404 {
		t.Fatal("closed lease image remains public")
	}
	status, got, _ := imageURLTestGet(t, http.MethodGet, secondURL)
	if status != 200 || !bytes.Equal(got, raw) {
		t.Fatal("closing one lease removed another lease's image")
	}
	if _, err := first.Upload(context.Background(), raw, "image/png"); err == nil {
		t.Fatal("closed lease accepted publication")
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	tunnel := m.tunnels["cloudflare"]
	m.mu.Unlock()
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	if len(tunnel.images) != 0 || tunnel.bytes != 0 {
		t.Fatal("lease cleanup retained images")
	}
}

func TestImageURLBackendValidationAndBounds(t *testing.T) {
	m, _ := imageURLTestManager(t)
	if _, err := m.NewLease(context.Background(), "invalid", ""); err == nil {
		t.Fatal("unknown backend accepted")
	}
	if _, err := m.NewLease(context.Background(), "litterbox", "2h"); err == nil {
		t.Fatal("unknown TTL accepted")
	}
	lease, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		raw  []byte
		mime string
	}{{[]byte("<html>not an image</html>"), "image/png"}, {imageURLTestPNG(t), "image/jpeg"}, {imageURLTestPNG(t), "text/html"}, {make([]byte, imageAttachmentMaxBytes+1), "image/png"}} {
		if _, err := lease.Upload(context.Background(), test.raw, test.mime); err == nil {
			t.Fatal("invalid image was published")
		}
	}
	for i := 0; i < 64; i++ {
		if _, err := lease.Upload(context.Background(), imageURLTestPNG(t), "image/png"); err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
	}
	if _, err := lease.Upload(context.Background(), imageURLTestPNG(t), "image/png"); err == nil {
		t.Fatal("lease count limit not enforced")
	}
	tunnel := lease.(*managedImageURLLease).tunnel
	tunnel.mu.Lock()
	saved := tunnel.bytes
	tunnel.bytes = imageURLStoreLimit
	tunnel.mu.Unlock()
	if _, err := tunnel.put(imageURLTestPNG(t), "image/png"); err == nil {
		t.Fatal("RAM limit not enforced")
	}
	tunnel.mu.Lock()
	tunnel.bytes = saved
	tunnel.mu.Unlock()
	for i := 1; i < imageURLLeaseLimit; i++ {
		if _, err := m.NewLease(context.Background(), "litterbox", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.NewLease(context.Background(), "litterbox", ""); err == nil {
		t.Fatal("active lease limit not enforced")
	}
}

func TestImageURLBackendRestartAndShutdown(t *testing.T) {
	m, starts := imageURLTestManager(t)
	old, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	oldTunnel := old.(*managedImageURLLease).tunnel
	_ = oldTunnel.process.Close()
	fresh, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatal("dead tunnel was not replaced")
	}
	if _, err := old.Upload(context.Background(), imageURLTestPNG(t), "image/png"); err == nil {
		t.Fatal("old lease published through a replacement process")
	}
	newURL, err := fresh.Upload(context.Background(), imageURLTestPNG(t), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NewLease(context.Background(), "cloudflare", ""); err == nil {
		t.Fatal("manager reopened after shutdown")
	}
	client := imageURLHTTPClient()
	client.Timeout = time.Second
	if resp, err := client.Get(newURL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("local server remained open after manager shutdown")
	}
}

func TestImageURLBackendStartupCancellationAndCoalescing(t *testing.T) {
	t.Run("manager shutdown cancels startup", func(t *testing.T) {
		entered := make(chan struct{})
		m := &imageURLBackendManager{deps: &imageURLBackendDeps{start: func(ctx context.Context, _ string, _ string) (imageURLTunnelProcess, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}}
		result := make(chan error, 1)
		go func() { _, err := m.NewLease(context.Background(), "cloudflare", ""); result <- err }()
		<-entered
		closed := make(chan struct{})
		go func() { _ = m.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown blocked behind startup")
		}
		if err := <-result; err == nil {
			t.Fatal("startup published after shutdown")
		}
	})
	t.Run("single startup for parallel leases", func(t *testing.T) {
		m, starts := imageURLTestManager(t)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l, err := m.NewLease(context.Background(), "cloudflare", "")
				if err != nil {
					t.Error(err)
					return
				}
				_ = l.Close(context.Background())
			}()
		}
		wg.Wait()
		if starts.Load() != 1 {
			t.Fatalf("parallel startup count %d", starts.Load())
		}
	})
	t.Run("public probe is required", func(t *testing.T) {
		var child *imageURLTestProcess
		m := &imageURLBackendManager{deps: &imageURLBackendDeps{client: &http.Client{Transport: imageURLTestTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("wrong image")), Header: make(http.Header)}, nil
		})}, start: func(_ context.Context, _ string, origin string) (imageURLTunnelProcess, error) {
			child = &imageURLTestProcess{origin: origin, done: make(chan struct{})}
			return child, nil
		}}}
		defer m.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := m.NewLease(ctx, "cloudflare", ""); err == nil {
			t.Fatal("unverified public endpoint accepted")
		}
		if child.closes.Load() != 1 {
			t.Fatal("failed readiness leaked child process")
		}
	})
}

func TestImageURLLitterboxMultipartTTLAndNoDeletion(t *testing.T) {
	for _, ttl := range []string{"", "1h", "12h", "24h", "72h"} {
		t.Run("TTL"+ttl, func(t *testing.T) {
			raw := imageURLTestPNG(t)
			calls := 0
			chosen := ttl
			if chosen == "" {
				chosen = "1h"
			}
			transport := imageURLTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.String() != imageURLLitterboxEndpoint {
					t.Fatal("wrong Litterbox endpoint or method")
				}
				if req.Header.Get("Authorization") != "" {
					t.Fatal("unexpected authentication")
				}
				if err := req.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				defer req.MultipartForm.RemoveAll()
				if req.FormValue("reqtype") != "fileupload" || req.FormValue("time") != chosen {
					t.Fatal("wrong multipart fields")
				}
				file, header, err := req.FormFile("fileToUpload")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				got, _ := io.ReadAll(file)
				if !bytes.Equal(got, raw) || header.Filename != "image.png" {
					t.Fatal("Litterbox image bytes or filename changed")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("https://litter.catbox.moe/example.png\n")), Header: make(http.Header)}, nil
			})
			m := &imageURLBackendManager{deps: &imageURLBackendDeps{client: &http.Client{Transport: transport}}}
			defer m.Close()
			lease, err := m.NewLease(context.Background(), "litterbox", ttl)
			if err != nil {
				t.Fatal(err)
			}
			duration, _ := imageURLLitterboxTTL(chosen)
			if remaining := time.Until(lease.ExpiresAt()); remaining > duration || remaining < duration-time.Minute {
				t.Fatal("wrong expiry")
			}
			got, err := lease.Upload(context.Background(), raw, "image/png")
			if err != nil {
				t.Fatal(err)
			}
			if got != "https://litter.catbox.moe/example.png" {
				t.Fatal("wrong published URL")
			}
			_ = lease.Close(context.Background())
			if calls != 1 {
				t.Fatal("attempted unsupported early deletion")
			}
		})
	}
}

func TestImageURLLitterboxRejectsUnsafeResponses(t *testing.T) {
	for _, response := range []string{"http://litter.catbox.moe/x.png", "https://evil.test/x.png", "https://litter.catbox.moe.evil.test/x.png", "https://user@litter.catbox.moe/x.png", "https://litter.catbox.moe/x.png?secret=1", "https://litter.catbox.moe/", "https://litter.catbox.moe/a/b", "<html>quota exhausted</html>", strings.Repeat("x", imageUploadURLBudget+1)} {
		t.Run(response[:min(len(response), 40)], func(t *testing.T) {
			m := &imageURLBackendManager{deps: &imageURLBackendDeps{client: &http.Client{Transport: imageURLTestTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
			})}}}
			defer m.Close()
			l, err := m.NewLease(context.Background(), "litterbox", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.Upload(context.Background(), imageURLTestPNG(t), "image/png"); err == nil {
				t.Fatal("unsafe upload response accepted")
			}
		})
	}
}

func TestImageURLTailscaleConflicts(t *testing.T) {
	for _, raw := range []string{`{"TCP":{"8443":{"HTTPS":true}}}`, `{"Web":{"private.example.ts.net:8443":{"Handlers":{}}}}`, `{"AllowFunnel":{"private.example.ts.net:8443":true}}`, `{"Foreground":{"sensitive-session":{"TCP":{"8443":{"HTTPS":true}}}}}`, `{"TCP":"invalid-shape"}`, `invalid`} {
		err := imageURLCheckTailscaleConfig([]byte(raw))
		if err == nil {
			t.Fatal("unsafe existing configuration accepted")
		}
		if strings.Contains(err.Error(), "private.example") || strings.Contains(err.Error(), "sensitive-session") {
			t.Fatal("private configuration leaked in error")
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"TCP":{"443":{"HTTPS":true}},"Web":{"private.ts.net:443":{}},"Foreground":{"other":{"TCP":{"10000":{}}}}}`} {
		if err := imageURLCheckTailscaleConfig([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if !imageURLTailscalePortAllowed(map[string]bool{"https://tailscale.com/cap/funnel-ports?ports=443,8443,10000": true}) {
		t.Fatal("allowed port rejected")
	}
	if imageURLTailscalePortAllowed(map[string]bool{"https://evil.test/cap/funnel-ports?ports=8443": true}) {
		t.Fatal("untrusted capability accepted")
	}
}

func TestImageURLProcessOutputAndEnvironmentIsolation(t *testing.T) {
	writer := &imageURLOriginWriter{urls: make(chan string, 1)}
	for _, invalid := range []string{"https://good.trycloudflare.com.evil.test", "http://good.trycloudflare.com", "https://good.trycloudflare.com:443", "https://good.trycloudflare.com/path", "https://good.trycloudflare.com?secret=x", "https://good.trycloudflare.com#secret", "https://user@good.trycloudflare.com"} {
		_, _ = writer.Write([]byte("secret=very-sensitive " + invalid + "\n"))
		select {
		case got := <-writer.urls:
			t.Fatalf("unsafe tunnel origin: %s", got)
		default:
		}
	}
	_, _ = writer.Write([]byte(strings.Repeat("z", 100000)))
	if len(writer.partial) > 4096 {
		t.Fatal("unbounded process output")
	}
	_, _ = writer.Write([]byte("\n| https://random-123.trycloud"))
	_, _ = writer.Write([]byte("flare.com |\n"))
	select {
	case got := <-writer.urls:
		if got != "https://random-123.trycloudflare.com" {
			t.Fatal(got)
		}
	default:
		t.Fatal("valid origin not parsed")
	}
	t.Setenv("TUNNEL_TOKEN", "never-pass-this")
	t.Setenv("TUNNEL_URL", "http://private-origin")
	t.Setenv("OPENAI_API_KEY", "never-pass-this-either")
	t.Setenv("XDG_CONFIG_HOME", "/private/user-config")
	env := imageURLSafeProcessEnv("/isolated")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "never-pass") || strings.Contains(joined, "private-origin") || strings.Contains(joined, "/private/user-config") {
		t.Fatal("inherited credential or configuration escaped isolation")
	}
	if !strings.Contains(joined, "HOME=/isolated") || !strings.Contains(joined, "XDG_CONFIG_HOME=/isolated") {
		t.Fatal("configuration home not isolated")
	}
	client := imageURLHTTPClient()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient HTTP proxy inherited")
	}
	if !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("redirects enabled")
	}
}

func TestImageURLReadCommandBoundedAndRedacted(t *testing.T) {
	// Real helper subprocess exercises output-drain and context lifecycle seams.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "KILO_IMAGE_TEST_HELPER=output")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = imageURLReadCommand(ctx, binary, env, "-test.run=^TestImageURLProcessHelper$")
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("oversized process status leaked or accepted")
	}
}
func TestImageURLProcessHelper(t *testing.T) {
	if os.Getenv("KILO_IMAGE_TEST_HELPER") == "output" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("secret-value", 100000))
		os.Exit(0)
	}
	if os.Getenv("KILO_IMAGE_TEST_HELPER") == "foreground" {
		for {
			time.Sleep(time.Second)
		}
	}
}
func TestImageURLForegroundProcessCleanup(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestImageURLProcessHelper$")
	cmd.Env = append(os.Environ(), "KILO_IMAGE_TEST_HELPER=foreground")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &imageURLCommandProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("owned foreground child remained alive")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImageURLLeaseConcurrentCloseUpload(t *testing.T) {
	m, _ := imageURLTestManager(t)
	l, err := m.NewLease(context.Background(), "cloudflare", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := imageURLTestPNG(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = l.Upload(context.Background(), raw, "image/png") }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = l.Close(context.Background()) }()
	wg.Wait()
	tunnel := l.(*managedImageURLLease).tunnel
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	if len(tunnel.images) != 0 {
		t.Fatal("concurrent close leaked published image")
	}
}

func TestImageURLRequestConcurrencyBound(t *testing.T) {
	tunnel := &imageURLTunnel{images: make(map[string]imageURLStoredImage), requests: make(chan struct{}, 1)}
	tunnel.requests <- struct{}{}
	recorder := httptest.NewRecorder()
	tunnel.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/"+strings.Repeat("a", 64), nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("request concurrency was not bounded")
	}
}

func TestImageURLTailscaleConfigInspectionReadOnly(t *testing.T) {
	// Keeping the complete input byte-for-byte is an invariant: the preflight
	// never normalizes or serializes the user's configuration back to tailscaled.
	raw := []byte(`{"TCP":{"443":{"HTTPS":true}},"SecretSettings":{"key":"secret"},"Foreground":{}}`)
	before := bytes.Clone(raw)
	if err := imageURLCheckTailscaleConfig(raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatal("configuration mutated")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("configuration corrupted")
	}
}

func TestImageURLTailscalePreflightRequiresExistingCapabilities(t *testing.T) {
	validStatus := `{"BackendState":"Running","Self":{"DNSName":"device.example.ts.net.","CapMap":{"https":[],"funnel":[],"https://tailscale.com/cap/funnel-ports?ports=443,8443,10000":[]}},"SecretSettings":{"key":"never-log-this"}}`
	cases := []struct {
		name, status, config string
		ok                   bool
	}{
		{"ready", validStatus, `{"TCP":{"443":{"HTTPS":true}}}`, true},
		{"port conflict", validStatus, `{"Foreground":{"other":{"TCP":{"8443":{"HTTPS":true}}}}}`, false},
		{"logged out", strings.Replace(validStatus, "Running", "NeedsLogin", 1), `{}`, false},
		{"funnel not enabled", strings.Replace(validStatus, `"funnel":[],`, "", 1), `{}`, false},
		{"HTTPS not enabled", strings.Replace(validStatus, `"https":[],`, "", 1), `{}`, false},
		{"8443 forbidden", strings.Replace(validStatus, "443,8443,10000", "443", 1), `{}`, false},
		{"bad DNS", strings.Replace(validStatus, "device.example.ts.net.", "device.evil.test.", 1), `{}`, false},
		{"malformed", `invalid`, "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var commands [][]string
			read := func(args ...string) ([]byte, error) {
				commands = append(commands, append([]string(nil), args...))
				if reflect.DeepEqual(args, []string{"status", "--json", "--peers=false"}) {
					return []byte(test.status), nil
				}
				if reflect.DeepEqual(args, []string{"serve", "status", "--json"}) {
					return []byte(test.config), nil
				}
				t.Fatalf("preflight attempted non-read command: %v", args)
				return nil, nil
			}
			got, err := imageURLInspectTailscale(read)
			if (err == nil) != test.ok {
				t.Fatalf("result %q, error %v", got, err)
			}
			if test.ok && got != "https://device.example.ts.net:8443" {
				t.Fatal("wrong public origin")
			}
			if err != nil && strings.Contains(err.Error(), "never-log-this") {
				t.Fatal("private status leaked")
			}
			if len(commands) > 2 {
				t.Fatal("unexpected preflight mutation")
			}
		})
	}
}

func TestImageURLExecutableDiscoveryUsesAbsoluteNativeFile(t *testing.T) {
	dir := t.TempDir()
	filename := "cloudflared"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := imageURLFindExecutable("cloudflared"); filepath.Clean(got) != filepath.Clean(path) {
		t.Fatalf("executable not found: %q", got)
	}
}

func TestImageURLLitterboxCloseCancelsInflightUpload(t *testing.T) {
	entered := make(chan struct{})
	m := &imageURLBackendManager{deps: &imageURLBackendDeps{client: &http.Client{Transport: imageURLTestTransport(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}}}
	defer m.Close()
	l, err := m.NewLease(context.Background(), "litterbox", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := imageURLTestPNG(t)
	result := make(chan error, 1)
	go func() { _, err := l.Upload(context.Background(), raw, "image/png"); result <- err }()
	<-entered
	if err = l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed upload succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("lease closure did not cancel upload")
	}
}

func TestImageURLBackendShutdownClosesChildReturningFromStartup(t *testing.T) {
	entered := make(chan struct{})
	child := &imageURLTestProcess{done: make(chan struct{})}
	m := &imageURLBackendManager{deps: &imageURLBackendDeps{start: func(ctx context.Context, _ string, origin string) (imageURLTunnelProcess, error) {
		child.origin = origin
		close(entered)
		<-ctx.Done()
		return child, nil
	}}}
	result := make(chan error, 1)
	go func() { _, err := m.NewLease(context.Background(), "cloudflare", ""); result <- err }()
	<-entered
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("lease returned after shutdown")
	}
	if child.closes.Load() != 1 {
		t.Fatal("startup race leaked owned process")
	}
}

func TestImageURLLitterboxRejectionIncludesOnlySafeStatus(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			m := &imageURLBackendManager{deps: &imageURLBackendDeps{client: &http.Client{Transport: imageURLTestTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("secret-body-value")), Header: http.Header{"Set-Cookie": []string{"secret-cookie-value"}}}, nil
			})}}}
			defer m.Close()
			l, err := m.NewLease(context.Background(), "litterbox", "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = l.Upload(context.Background(), imageURLTestPNG(t), "image/png")
			var safe *imageUploadRequestError
			if !errors.As(err, &safe) || safe.status != http.StatusBadGateway || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("missing actionable typed service status: %v", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("service response leaked into error")
			}
			if calls != 1 {
				t.Fatal("service rejection retried or fell back automatically")
			}
		})
	}
}
