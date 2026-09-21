package main

import (
	"bufio"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func historySample(t *testing.T, account, payload string) *usageObserver {
	t.Helper()
	u := observeUsage(t, false, payload)
	u.historyAccount = account
	return u
}

func TestUsageHistoryPersistsLocalDaysAndExactMoneyWithoutTraffic(t *testing.T) {
	dir := t.TempDir()
	store := newUsageHistoryStore(dir)
	account := usageAccountID("private-api-key", "private-org")
	zone := time.FixedZone("test local", 2*3600)
	now := time.Date(2026, 10, 1, 0, 10, 0, 0, zone)
	paid := historySample(t, account, `{"usage":{"input_tokens":100,"output_tokens":4,"cost":0.000000001,"input_tokens_details":{"cached_tokens":80}}}`)
	paid.label, paid.org = "private conversation name", "private-org"
	store.record(account, paid, now.Add(-20*time.Minute))
	store.record(account, paid, now)
	store.record(account, historySample(t, account, `{"usage":{"input_tokens":7}}`), now)
	store.flush()
	reopened := newUsageHistoryStore(dir)
	got := reopened.snapshot(account, now)
	if got.Error != "" || got.Today.Requests != 2 || got.Today.Priced != 1 || got.Today.CostUSD != "0.000000001" || got.Yesterday.Requests != 1 || got.Last7Days.CostUSD != "0.000000002" || got.Last7Days.Cached != 160 || len(got.Days) != 2 || got.StartedAt == "" {
		t.Fatalf("incorrect persisted calendar totals: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "usage-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-api-key", "private-org", "private conversation name", "authorization", "input_tokens", "lastCache"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("history contains sensitive/raw request data: %s", secret)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "usage-history.json"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("history must be private")
	}
	if reopened.snapshot(usageAccountID("other-key", "private-org"), now).Today.Requests != 0 || reopened.snapshot(usageAccountID("private-api-key", "other-org"), now).Today.Requests != 0 {
		t.Fatal("cross-account history leak")
	}
	// Continue after restart: costNanos must be restored, not reset to zero.
	reopened.record(account, paid, now)
	reopened.flush()
	if reopened.snapshot(account, now).Today.CostUSD != "0.000000002" {
		t.Fatal("restart lost nanodollars")
	}
}

func TestUsageHistoryCalendarDSTAndSevenDays(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	store := newUsageHistoryStore(t.TempDir())
	now := time.Date(2026, 10, 26, 0, 30, 0, 0, zone)
	account := usageAccountID("key", "org")
	u := historySample(t, account, `{"usage":{"cost":0.1}}`)
	for _, days := range []int{0, -1, -6, -7} {
		store.record(account, u, now.AddDate(0, 0, days))
	}
	store.flush()
	got := store.snapshot(account, now)
	if got.Yesterday.Requests != 1 || got.Last7Days.Requests != 3 || got.Last7Days.CostUSD != "0.300000000" {
		t.Fatalf("DST calendar grouping: %+v", got)
	}
}

func TestUsageHistoryCorruptionPreservedAndWriteFailureVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage-history.json")
	bad := []byte(`{"schemaVersion":99,"scopes":{}}`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	s := newUsageHistoryStore(dir)
	account := usageAccountID("key", "org")
	u := historySample(t, account, `{"usage":{"cost":0.2}}`)
	s.record(account, u, time.Now())
	s.flush()
	data, _ := os.ReadFile(path)
	if string(data) != string(bad) || s.snapshot(account, time.Now()).Error == "" {
		t.Fatal("corrupt history was silently overwritten")
	}
	s = newUsageHistoryStore(t.TempDir())
	s.write = func(string, []byte) error { return errors.New("disk full") }
	s.record(account, u, time.Now())
	s.flush()
	if s.snapshot(account, time.Now()).Error == "" {
		t.Fatal("save failure hidden")
	}
	s.write = atomicCatalogFile
	s.record(account, u, time.Now())
	s.flush()
	if got := s.snapshot(account, time.Now()); got.Error != "" || got.Today.CostUSD != "0.400000000" {
		t.Fatalf("failed to retry accumulated totals: %+v", got)
	}
}

func TestUsageHistoryConcurrentCompletions(t *testing.T) {
	s := newUsageHistoryStore(t.TempDir())
	account := usageAccountID("key", "org")
	u := historySample(t, account, `{"usage":{"cost":0.000001}}`)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() { defer wg.Done(); s.record(account, u, time.Now()); _ = s.snapshot(account, time.Now()) }()
	}
	wg.Wait()
	s.flush()
	got := newUsageHistoryStore(s.dir).snapshot(account, time.Now())
	if got.Today.Requests != 100 || got.Today.CostUSD != "0.000100000" {
		t.Fatalf("lost concurrent completions: %+v", got)
	}
}

func TestUsageHistoryProxyAccountingWhileCapturesDisabled(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "test-key", "test-org"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"usage": map[string]any{"cost": 0.125}})
	}))
	defer up.Close()
	setUpstream(a, up.URL)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"private text"}]}`))
	r.Header.Set("Authorization", "Bearer local-secret")
	w := httptest.NewRecorder()
	a.inferenceHandler(a.apiKey, a.config.OrgID, "local-secret", "127.0.0.1:8877").ServeHTTP(w, r)
	a.usageHistory.flush()
	got := a.usageHistorySnapshotLocked()
	if w.Code != 200 || got.Today.Requests != 1 || got.Today.CostUSD != "0.125000000" || len(a.events) != 0 || len(a.traces) != 0 {
		t.Fatalf("capture-independent accounting failed: %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(a.dir, "usage-history.json"))
	if strings.Contains(string(data), "private text") {
		t.Fatal("prompt persisted")
	}
}

func TestUsageHistoryStopWaitsForCancelledStreamingAccounting(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "key", "org"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"cost\":0.2}}}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer up.Close()
	setUpstream(a, up.URL)
	local := httptest.NewServer(a.inferenceHandler("key", "org", "local", "127.0.0.1:8877"))
	defer local.Close()
	a.proxyServer = local.Config
	r, _ := http.NewRequest("POST", local.URL+"/v1/responses", strings.NewReader(`{"model":"test","input":"not retained","stream":true}`))
	r.Host = "127.0.0.1:8877"
	r.Header.Set("Authorization", "Bearer local")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for range 2 {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	a.stop()
	got := newUsageHistoryStore(a.dir).snapshot(usageAccountID("key", "org"), time.Now())
	if got.Today.Requests != 1 || got.Today.CostUSD != "0.200000000" {
		t.Fatalf("quit lost completed SSE accounting: %+v", got)
	}
}
