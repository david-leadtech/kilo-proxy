package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Only a validated public origin survives the process output parser. All other
// output (including status settings, credentials and bearer image paths) is
// discarded. The writer keeps at most one bounded line and never blocks pipes.
var imageURLCloudflareOrigin = regexp.MustCompile(`https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.trycloudflare\.com(?:[ \t\x22\x27|),\]}]|$)`)
var imageURLTailscaleHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\.ts\.net$`)

type imageURLOriginWriter struct {
	mu      sync.Mutex
	partial []byte
	urls    chan string
}

func (w *imageURLOriginWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			w.inspect()
			w.partial = w.partial[:0]
		} else if len(w.partial) < 4096 {
			w.partial = append(w.partial, b)
		}
	}
	return len(p), nil
}
func (w *imageURLOriginWriter) inspect() {
	value := imageURLCloudflareOrigin.Find(w.partial)
	if value == nil {
		return
	}
	raw := strings.TrimRight(string(value), " \t\"'|),]}")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || !strings.HasSuffix(parsed.Hostname(), ".trycloudflare.com") {
		return
	}
	select {
	case w.urls <- parsed.Scheme + "://" + parsed.Host:
	default:
	}
}

type imageURLCommandProcess struct {
	cmd       *exec.Cmd
	origin    string
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (p *imageURLCommandProcess) URL() string           { return p.origin }
func (p *imageURLCommandProcess) Done() <-chan struct{} { return p.done }
func (p *imageURLCommandProcess) Close() error {
	p.closeOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		// Tailscale foreground Serve is scoped to this CLI's WatchIPNBus connection.
		// Closing that connection removes only our ephemeral config. Never invoke
		// funnel reset/off or rewrite any background Serve/Funnel settings.
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
			return
		case <-time.After(2 * time.Second):
		}
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			p.closeErr = errors.New("The owned image tunnel process did not exit promptly.")
		}
	})
	return p.closeErr
}

func imageURLSafeProcessEnv(isolatedHome string) []string {
	// Do not inherit API tokens, TUNNEL_* flags, cloud credentials or an alternate
	// origin from the parent environment. The explicit command selects the route.
	allowed := map[string]bool{"PATH": true, "HOME": true, "USERPROFILE": true, "SYSTEMROOT": true, "WINDIR": true, "TEMP": true, "TMP": true, "TMPDIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "XDG_RUNTIME_DIR": true}
	env := make([]string, 0, 16)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if allowed[strings.ToUpper(key)] && !(isolatedHome != "" && (key == "HOME" || key == "USERPROFILE")) {
			env = append(env, item)
		}
	}
	if isolatedHome != "" {
		env = append(env, "HOME="+isolatedHome, "USERPROFILE="+isolatedHome, "XDG_CONFIG_HOME="+isolatedHome, "XDG_DATA_HOME="+isolatedHome)
	}
	return env
}

// GUI launches do not always inherit a shell PATH. Search ordinary install
// locations without installing tools or executing shell wrappers.
func imageURLFindExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil && filepath.IsAbs(path) && launchExecutable(path) && (runtime.GOOS != "windows" || strings.EqualFold(filepath.Ext(path), ".exe")) {
		return path
	}
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
		dirs = nil
		for _, key := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			root := os.Getenv(key)
			if !filepath.IsAbs(root) {
				continue
			}
			dirs = append(dirs, filepath.Join(root, "Tailscale"), filepath.Join(root, "cloudflared"), filepath.Join(root, "Microsoft", "WinGet", "Links"))
		}
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, file)
		if launchExecutable(path) {
			return path
		}
	}
	return ""
}

func startImageURLTunnelProcess(ctx context.Context, backend, origin string) (imageURLTunnelProcess, error) {
	binaryName := "cloudflared"
	if backend == "tailscale" {
		binaryName = "tailscale"
	}
	binary := imageURLFindExecutable(binaryName)
	if binary == "" {
		if backend == "tailscale" {
			return nil, errors.New("Tailscale image URLs require the tailscale command on PATH, a signed-in account, HTTPS and Funnel enabled, and HTTPS port 8443 free.")
		}
		return nil, errors.New("Cloudflare image URLs require cloudflared installed and available on PATH. No Cloudflare account is required.")
	}
	var err error
	var args []string
	var tempDir, publicOrigin string
	env := imageURLSafeProcessEnv("")
	if backend == "cloudflare" {
		tempDir, err = os.MkdirTemp("", "kilo-image-cloudflared-")
		if err != nil {
			return nil, errors.New("Could not create the isolated Cloudflare tunnel configuration.")
		}
		configPath := filepath.Join(tempDir, "config.yml")
		if err = os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
			_ = os.RemoveAll(tempDir)
			return nil, errors.New("Could not create the isolated Cloudflare tunnel configuration.")
		}
		env = imageURLSafeProcessEnv(tempDir)
		// Supplying an explicit empty file also prevents reading /etc/cloudflared or
		// a user's named-tunnel configuration. The metrics listener is loopback only.
		args = []string{"tunnel", "--config", configPath, "--no-autoupdate", "--url", origin, "--metrics", "127.0.0.1:0", "--loglevel", "info"}
	} else {
		publicOrigin, err = imageURLTailscalePreflight(ctx, binary, env)
		if err != nil {
			return nil, err
		}
		args = []string{"funnel", "--https=8443", origin}
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	output := &imageURLOriginWriter{urls: make(chan string, 1)}
	if backend == "cloudflare" {
		cmd.Stdout = output
		cmd.Stderr = output
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if err = ctx.Err(); err != nil {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		return nil, errors.New("Could not start the installed image tunnel executable.")
	}
	p := &imageURLCommandProcess{cmd: cmd, origin: publicOrigin, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		close(p.done)
	}()
	if backend == "tailscale" {
		return p, nil
	}
	select {
	case result := <-output.urls:
		p.origin = result
		return p, nil
	case <-ctx.Done():
		_ = p.Close()
		return nil, errors.New("Cloudflare did not provide a quick tunnel before the startup deadline. Check cloudflared and your connection.")
	case <-p.done:
		return nil, errors.New("Cloudflare exited before creating a quick tunnel. Check cloudflared and your connection.")
	}
}

// Preflight and command output are deliberately never included in errors; they
// can contain machine identities, private routes or sensitive settings.
func imageURLTailscalePreflight(ctx context.Context, binary string, env []string) (string, error) {
	return imageURLInspectTailscale(func(args ...string) ([]byte, error) { return imageURLReadCommand(ctx, binary, env, args...) })
}

func imageURLInspectTailscale(read func(...string) ([]byte, error)) (string, error) {
	status, err := read("status", "--json", "--peers=false")
	if err != nil {
		return "", errors.New("Could not read Tailscale status. Start Tailscale and sign in before using image URLs.")
	}
	var state struct {
		BackendState string
		Self         *struct {
			DNSName      string
			Capabilities []string
			CapMap       map[string]json.RawMessage
		}
	}
	if json.Unmarshal(status, &state) != nil || state.BackendState != "Running" || state.Self == nil {
		return "", errors.New("Tailscale must be running and signed in before using image URLs.")
	}
	hostname := strings.TrimSuffix(state.Self.DNSName, ".")
	if !imageURLTailscaleHostname.MatchString(hostname) {
		return "", errors.New("Tailscale did not provide a valid HTTPS device name.")
	}
	caps := make(map[string]bool)
	for _, value := range state.Self.Capabilities {
		caps[value] = true
	}
	for value := range state.Self.CapMap {
		caps[value] = true
	}
	if !caps["https"] || !caps["funnel"] || !imageURLTailscalePortAllowed(caps) {
		return "", errors.New("Enable Tailscale HTTPS and Funnel for this device, including port 8443, before using image URLs.")
	}
	config, err := read("serve", "status", "--json")
	if err != nil {
		return "", errors.New("Could not inspect Tailscale Serve/Funnel configuration. No settings were changed.")
	}
	if err = imageURLCheckTailscaleConfig(config); err != nil {
		return "", err
	}
	return "https://" + hostname + ":8443", nil
}

func imageURLTailscalePortAllowed(caps map[string]bool) bool {
	for capability := range caps {
		parsed, err := url.Parse(capability)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "tailscale.com" || parsed.Path != "/cap/funnel-ports" {
			continue
		}
		for _, part := range strings.Split(parsed.Query().Get("ports"), ",") {
			if part == "8443" {
				return true
			}
			lower, upper, ok := strings.Cut(part, "-")
			if ok {
				lo, e1 := strconv.Atoi(lower)
				hi, e2 := strconv.Atoi(upper)
				if e1 == nil && e2 == nil && lo > 0 && hi <= 65535 && lo <= 8443 && hi >= 8443 {
					return true
				}
			}
		}
	}
	return false
}

func imageURLCheckTailscaleConfig(raw []byte) error { return imageURLCheckTailscaleConfigDepth(raw, 0) }

func imageURLCheckTailscaleConfigDepth(raw []byte, depth int) error {
	if depth > 2 {
		return errors.New("The Tailscale Serve/Funnel configuration has unsupported nested foreground state. No settings were changed.")
	}
	var config struct {
		TCP         map[string]json.RawMessage
		Web         map[string]json.RawMessage
		AllowFunnel map[string]bool
		Foreground  map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &config) != nil {
		return errors.New("Could not safely read the existing Tailscale Serve/Funnel configuration. No settings were changed.")
	}
	conflict := func() error {
		return errors.New("Tailscale HTTPS port 8443 already has a Serve/Funnel configuration. Free that port yourself or choose another image URL method; existing settings were preserved.")
	}
	if _, ok := config.TCP["8443"]; ok {
		return conflict()
	}
	for host := range config.Web {
		if strings.HasSuffix(host, ":8443") {
			return conflict()
		}
	}
	for host, on := range config.AllowFunnel {
		if on && strings.HasSuffix(host, ":8443") {
			return conflict()
		}
	}
	for _, fg := range config.Foreground {
		if err := imageURLCheckTailscaleConfigDepth(fg, depth+1); err != nil {
			return err
		}
	}
	return nil
}

type imageURLBoundedOutput struct {
	data     []byte
	overflow bool
}

func (w *imageURLBoundedOutput) Write(p []byte) (int, error) {
	const limit = 1 << 20
	n := len(p)
	if len(p) > limit-len(w.data) {
		w.overflow = true
		p = p[:limit-len(w.data)]
	}
	w.data = append(w.data, p...)
	return n, nil
}
func imageURLReadCommand(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, binary, args...)
	cmd.Env = env
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	output := &imageURLBoundedOutput{}
	cmd.Stdout = output
	err := cmd.Run()
	if err != nil || output.overflow {
		return nil, errors.New("Could not inspect the tunnel executable status.")
	}
	return output.data, nil
}
