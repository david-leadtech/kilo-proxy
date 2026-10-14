package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func usageQueryTestRow(date string, dims map[string]string) string {
	b, _ := json.Marshal(dims)
	return fmt.Sprintf(`{"datetime":%q,"dimensions":%s,"costMicrodollars":123.000125,"requestCount":2,"inputTokens":9007199254740993,"outputTokens":20,"cacheWriteTokens":10,"cacheHitTokens":60,"errorCount":0}`, date, b)
}

func TestUsageQueryHistoricalCrossesFiltersAndRawCSV(t *testing.T) {
	q := usageQuery{StartDate: "2025-01-01", EndDate: "2025-01-09", GroupBy: []string{"model", "provider", "project"}, CostSource: "market", Models: []string{"vendor/one"}, Providers: []string{"provider"}, Projects: []string{"repo,\"ticket\"\nline"}, Features: []string{"cli"}, Modes: []string{"code"}}
	calls := 0
	transport := usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-KiloCode-OrganizationId") != "team" || r.URL.Path != "/api/trpc/usageAnalytics.getTable" {
			t.Fatal("wrong endpoint or authentication")
		}
		var input map[string]any
		_ = json.Unmarshal([]byte(r.URL.Query().Get("input")), &input)
		if input["viewAs"] != "self" || input["personalScope"] != "personal-only" || input["organizationId"] != "team" || input["costSource"] != "market" || input["granularity"] != "day" || input["limit"] != float64(10000) {
			t.Fatalf("wrong scope: %v", input)
		}
		for _, filter := range []string{"models", "providers", "projects", "features", "modes"} {
			if len(input[filter].([]any)) != 1 {
				t.Fatal("filter lost")
			}
		}
		start := input["startDate"].(string)
		wantEnd := "2025-01-07T23:59:59.999999999Z"
		if calls == 2 {
			wantEnd = "2025-01-09T23:59:59.999999999Z"
		}
		if input["endDate"] != wantEnd {
			t.Fatalf("inclusive end lost: %v", input)
		}
		row := usageQueryTestRow(start[:10], map[string]string{"model": "vendor/one", "provider": "provider", "project": q.Projects[0]})
		return billingTestResponse(200, billingTestUsage(row)), nil
	})
	got, err := fetchUsageQuery(context.Background(), "https://kilo.test", transport, "secret", "team", q, billingTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(got.Rows) != 2 || got.Scope != usageAccountID("secret", "team") {
		t.Fatalf("unexpected result: %+v", got)
	}
	data, err := usageQueryCSV(got)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1][3] != q.Projects[0] || rows[1][4] != "123.000125" || rows[1][6] != "9007199254740993" {
		t.Fatalf("CSV changed raw values: %q", data)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatal("credential reached CSV")
	}
}

func TestUsageQueryDoesNotInventMissingDaysOrZeroCosts(t *testing.T) {
	q := usageQuery{StartDate: "2026-09-01", EndDate: "2026-09-02", GroupBy: []string{}, CostSource: "cost"}
	transport := usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		var input map[string]any
		_ = json.Unmarshal([]byte(r.URL.Query().Get("input")), &input)
		if _, exists := input["organizationId"]; exists {
			t.Fatal("personal query included team")
		}
		return billingTestResponse(200, billingTestUsage("")), nil
	})
	got, err := fetchUsageQuery(context.Background(), "https://kilo.test", transport, "key", "", q, billingTestNow)
	if err != nil || got == nil || got.Rows == nil || len(got.Rows) != 0 {
		t.Fatalf("empty history invented rows: %v, %v", got, err)
	}
	data, _ := usageQueryCSV(got)
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("CSV should contain header only: %q", data)
	}
}

func TestUsageQuerySplitsCoarseOrCappedWindows(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprint(capped), func(t *testing.T) {
			calls := 0
			transport := usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var input map[string]any
				_ = json.Unmarshal([]byte(r.URL.Query().Get("input")), &input)
				if calls == 1 {
					if capped {
						rows := strings.TrimSuffix(strings.Repeat(usageQueryTestRow("2026-09-01", map[string]string{"model": "one"})+",", usageQueryRowLimit), ",")
						return billingTestResponse(200, billingTestUsage(rows)), nil
					}
					return billingTestResponse(200, `{"result":{"data":{"rows":[],"effectiveGranularity":"week"}}}`), nil
				}
				return billingTestResponse(200, billingTestUsage(usageQueryTestRow(input["startDate"].(string)[:10], map[string]string{"model": "one"}))), nil
			})
			got, err := fetchUsageQuery(context.Background(), "https://kilo.test", transport, "key", "", usageQuery{StartDate: "2026-09-01", EndDate: "2026-09-02"}, billingTestNow)
			if err != nil || calls != 3 || len(got.Rows) != 2 {
				t.Fatalf("split failed: calls=%d, result=%v, err=%v", calls, got, err)
			}
		})
	}
	transport := usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		return billingTestResponse(200, `{"result":{"data":{"rows":[],"effectiveGranularity":"week"}}}`), nil
	})
	got, err := fetchUsageQuery(context.Background(), "https://kilo.test", transport, "key", "", usageQuery{StartDate: "2026-09-01", EndDate: "2026-09-01"}, billingTestNow)
	if got != nil || err == nil || !strings.Contains(err.Error(), "single day") {
		t.Fatal("coarsened history presented as daily", got, err)
	}
}

func TestUsageQueryRejectsInvalidOrPartialRows(t *testing.T) {
	start, _ := time.Parse("2006-01-02", "2026-09-01")
	end := start.AddDate(0, 0, 1)
	valid := usageQueryTestRow("2026-09-01", map[string]string{"model": "one"})
	for _, row := range []string{
		strings.Replace(valid, `123.000125`, `null`, 1), strings.Replace(valid, `123.000125`, `1e999999999`, 1), strings.Replace(valid, `"requestCount":2`, `"requestCount":-1`, 1), strings.Replace(valid, `"requestCount":2`, `"requestCount":1.5`, 1), strings.Replace(valid, `"outputTokens":20,`, ``, 1), strings.Replace(valid, `"2026-09-01"`, `"2026-09-01T01:00:00Z"`, 1), strings.Replace(valid, `"2026-09-01"`, `"2026-08-31"`, 1), strings.Replace(valid, `"model":"one"`, `"provider":"one"`, 1), valid + "," + valid,
	} {
		if _, _, err := parseUsageQueryRows(json.RawMessage(billingTestUsage(row)), []string{"model"}, start, end); err == nil {
			t.Fatalf("accepted invalid rows: %s", row)
		}
	}
	for _, raw := range []string{`{}`, `{"result":{"data":{"effectiveGranularity":"day"}}}`, `{"result":{"data":{"rows":null,"effectiveGranularity":"day"}}}`} {
		if _, _, err := parseUsageQueryRows(json.RawMessage(raw), []string{"model"}, start, end); err == nil {
			t.Fatal("missing rows accepted")
		}
	}
}

func TestUsageQueryValidationAndAuthentication(t *testing.T) {
	for _, q := range []usageQuery{{StartDate: "bad", EndDate: "2026-09-01"}, {StartDate: "2024-01-01", EndDate: "2026-09-01"}, {StartDate: "2026-09-01", EndDate: "2026-10-01"}, {StartDate: "2026-09-01", EndDate: "2026-09-01", GroupBy: []string{"model", "model"}}, {StartDate: "2026-09-01", EndDate: "2026-09-01", GroupBy: []string{"model", "project", "feature", "mode"}}, {StartDate: "2026-09-01", EndDate: "2026-09-01", CostSource: "unknown"}} {
		if _, _, err := q.validate(billingTestNow); err == nil {
			t.Fatal("invalid query accepted", q)
		}
	}
	a := testApp(t)
	a.adminHost = "127.0.0.1:54321"
	a.adminToken = "admin"
	for _, tc := range []struct {
		method, token, body string
		want                int
	}{{"GET", "admin", "", 405}, {"POST", "wrong", "{}", 401}, {"POST", "admin", `{"startDate":"2026-09-01","endDate":"2026-09-01","viewAs":"org-wide"}`, 400}} {
		r := httptest.NewRequest(tc.method, "http://"+a.adminHost+"/api/usage/query", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.adminHandler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d, want %d: %s", w.Code, tc.want, w.Body.String())
		}
	}
}

func TestUsageQueryDiscardsResultsAfterAccountChangeAndFailedWindow(t *testing.T) {
	a := testApp(t)
	a.apiKey = "key"
	a.config.OrgID = "one"
	a.accountURL = "https://kilo.test"
	a.transport = usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		a.mu.Lock()
		a.config.OrgID = "two"
		a.mu.Unlock()
		return billingTestResponse(200, billingTestUsage("")), nil
	})
	r := httptest.NewRequest("POST", "http://local/api/usage/query", strings.NewReader(`{"startDate":"2026-09-01","endDate":"2026-09-01"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.usageQueryAPI(w, r)
	if w.Code != 409 || strings.Contains(w.Body.String(), `"rows"`) {
		t.Fatal("old account rows escaped", w.Code, w.Body.String())
	}
	calls := 0
	transport := usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return billingTestResponse(429, "{}"), nil
		}
		return billingTestResponse(200, billingTestUsage(usageQueryTestRow("2026-09-01", map[string]string{"model": "one"}))), nil
	})
	got, err := fetchUsageQuery(context.Background(), "https://kilo.test", transport, "key", "", usageQuery{StartDate: "2026-09-01", EndDate: "2026-09-10"}, billingTestNow)
	if err == nil || got != nil {
		t.Fatal("partial rows returned after error")
	}
}
