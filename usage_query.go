package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const usageQueryRowLimit = 10000
const usageQueryTotalLimit = 50000
const usageQueryBytesLimit = 8 << 20

var usageDimensions = []string{"model", "provider", "project", "feature", "mode", "user"}
var usageMetricColumns = []string{"costMicrodollars", "requestCount", "inputTokens", "outputTokens", "cacheWriteTokens", "cacheHitTokens", "errorCount"}

type usageQuery struct {
	StartDate  string   `json:"startDate"`
	EndDate    string   `json:"endDate"`
	GroupBy    []string `json:"groupBy"`
	CostSource string   `json:"costSource"`
	Models     []string `json:"models,omitempty"`
	Providers  []string `json:"providers,omitempty"`
	Projects   []string `json:"projects,omitempty"`
	Features   []string `json:"features,omitempty"`
	Modes      []string `json:"modes,omitempty"`
}

// Numbers remain strings all the way to CSV and the browser. Converting them
// to float64 (or JavaScript Number) would corrupt large token totals and costs.
type usageQueryResult struct {
	Query       usageQuery `json:"query"`
	Scope       string     `json:"scope"`
	FetchedAt   string     `json:"fetchedAt"`
	TimeZone    string     `json:"timeZone"`
	Granularity string     `json:"granularity"`
	Columns     []string   `json:"columns"`
	Rows        [][]string `json:"rows"`
}

func (q *usageQuery) validate(now time.Time) (time.Time, time.Time, error) {
	start, e1 := time.Parse("2006-01-02", q.StartDate)
	end, e2 := time.Parse("2006-01-02", q.EndDate)
	today := now.UTC().Truncate(24 * time.Hour)
	if e1 != nil || e2 != nil || start.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || end.Before(start) || end.After(today) || end.Sub(start) >= 366*24*time.Hour {
		return start, end, errors.New("Choose valid UTC dates, up to 366 days per export, ending no later than today.")
	}
	if q.CostSource == "" {
		q.CostSource = "cost"
	}
	if q.CostSource != "cost" && q.CostSource != "market" {
		return start, end, errors.New("Choose Kilo charges or market cost.")
	}
	if q.GroupBy == nil {
		q.GroupBy = []string{"model"}
	}
	if len(q.GroupBy) > 3 {
		return start, end, errors.New("Choose at most three grouping dimensions.")
	}
	seen := map[string]bool{}
	for _, dim := range q.GroupBy {
		valid := false
		for _, allowed := range usageDimensions {
			valid = valid || dim == allowed
		}
		if !valid || seen[dim] {
			return start, end, errors.New("Invalid or repeated grouping dimension.")
		}
		seen[dim] = true
	}
	for _, filter := range [][]string{q.Models, q.Providers, q.Projects, q.Features, q.Modes} {
		if len(filter) > 50 {
			return start, end, errors.New("Use at most 50 values per filter.")
		}
		for _, value := range filter {
			if strings.TrimSpace(value) == "" || len(value) > 256 {
				return start, end, errors.New("Filter values must contain between 1 and 256 characters.")
			}
		}
	}
	return start, end.AddDate(0, 0, 1), nil
}

func (q usageQuery) input(start, end time.Time) map[string]any {
	input := map[string]any{"startDate": start.Format(time.RFC3339Nano), "endDate": end.Format(time.RFC3339Nano), "granularity": "day", "costSource": q.CostSource, "personalScope": "personal-only", "viewAs": "self", "groupBy": q.GroupBy, "limit": usageQueryRowLimit}
	for name, values := range map[string][]string{"models": q.Models, "providers": q.Providers, "projects": q.Projects, "features": q.Features, "modes": q.Modes} {
		if len(values) > 0 {
			input[name] = values
		}
	}
	return input
}

func usageQueryCSV(result *usageQueryResult) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	if err := w.Write(result.Columns); err != nil {
		return nil, err
	}
	for _, row := range result.Rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func usageQueryFilename(q usageQuery) string {
	return fmt.Sprintf("kilo-usage-%s-%s-%s.csv", q.StartDate, q.EndDate, q.CostSource)
}

func parseUsageQueryRows(raw json.RawMessage, group []string, start, end time.Time) ([][]string, string, error) {
	var envelope struct {
		Result *struct {
			Data *struct {
				Rows                 *[]json.RawMessage `json:"rows"`
				EffectiveGranularity string             `json:"effectiveGranularity"`
			} `json:"data"`
		} `json:"result"`
	}
	invalid := errors.New("Kilo returned incomplete or invalid usage rows. No partial CSV was produced.")
	if json.Unmarshal(raw, &envelope) != nil || envelope.Result == nil || envelope.Result.Data == nil || envelope.Result.Data.Rows == nil {
		return nil, "", invalid
	}
	data := envelope.Result.Data
	if data.EffectiveGranularity != "day" || len(*data.Rows) >= usageQueryRowLimit {
		return nil, data.EffectiveGranularity, nil
	}
	rows := make([][]string, 0, len(*data.Rows))
	seen := map[string]bool{}
	for _, record := range *data.Rows {
		var row map[string]json.RawMessage
		if json.Unmarshal(record, &row) != nil {
			return nil, "", invalid
		}
		var datetime string
		if json.Unmarshal(row["datetime"], &datetime) != nil {
			return nil, "", invalid
		}
		var at time.Time
		for _, format := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
			if value, err := time.Parse(format, datetime); err == nil {
				at = value.UTC()
				break
			}
		}
		if at.IsZero() || at.Before(start) || !at.Before(end) || !at.Equal(at.Truncate(24*time.Hour)) {
			return nil, "", invalid
		}
		var dims map[string]string
		if json.Unmarshal(row["dimensions"], &dims) != nil || len(dims) != len(group) {
			return nil, "", invalid
		}
		values := []string{datetime}
		for _, dim := range group {
			value, exists := dims[dim]
			if !exists {
				return nil, "", invalid
			}
			values = append(values, value)
		}
		// Use a JSON tuple instead of a delimiter: dimensions may contain any
		// label, including commas, quotes, newlines and delimiter characters.
		identity, _ := json.Marshal(append([]string{at.Format("2006-01-02")}, values[1:]...))
		if seen[string(identity)] {
			return nil, "", invalid
		}
		seen[string(identity)] = true
		for _, metric := range usageMetricColumns {
			value := strings.TrimSpace(string(row[metric]))
			if len(value) == 0 || len(value) > 64 || !json.Valid([]byte(value)) || value[0] < '0' || value[0] > '9' {
				return nil, "", invalid
			}
			if i := strings.IndexAny(value, "eE"); i >= 0 {
				exponent, err := strconv.Atoi(value[i+1:])
				if err != nil || exponent < -18 || exponent > 18 {
					return nil, "", invalid
				}
			}
			n, ok := new(big.Rat).SetString(value)
			if !ok || n.Sign() < 0 || n.Cmp(new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))) > 0 || (metric != "costMicrodollars" && !n.IsInt()) {
				return nil, "", invalid
			}
			values = append(values, value)
		}
		rows = append(rows, values)
	}
	return rows, "day", nil
}

func fetchUsageQuery(ctx context.Context, endpoint string, transport http.RoundTripper, key, org string, q usageQuery, now time.Time) (*usageQueryResult, error) {
	start, end, err := q.validate(now)
	if err != nil {
		return nil, err
	}
	result := &usageQueryResult{Query: q, Scope: usageAccountID(key, org), FetchedAt: now.UTC().Format(time.RFC3339), TimeZone: "UTC", Granularity: "day", Columns: append(append([]string{"datetime"}, q.GroupBy...), usageMetricColumns...), Rows: [][]string{}}
	var fetch func(time.Time, time.Time) error
	requests, bytesUsed := 0, 0
	fetch = func(from, to time.Time) error {
		requests++
		if requests > 200 {
			return errors.New("This export needs too many requests. Narrow the date range or grouping.")
		}
		input := q.input(from, to.Add(-time.Nanosecond))
		if org != "" {
			input["organizationId"] = org
		}
		encoded, _ := json.Marshal(input)
		var raw json.RawMessage
		if err := billingGetLimit(ctx, endpoint, transport, key, org, "/api/trpc/usageAnalytics.getTable?"+url.Values{"input": {string(encoded)}}.Encode(), &raw, usageQueryBytesLimit); err != nil {
			return err
		}
		rows, granularity, err := parseUsageQueryRows(raw, q.GroupBy, from, to)
		if err != nil {
			return err
		}
		if granularity != "day" || rows == nil {
			days := int(to.Sub(from) / (24 * time.Hour))
			if days <= 1 {
				return errors.New("Kilo capped or coarsened a single day. Narrow the filters; no truncated CSV was produced.")
			}
			middle := from.AddDate(0, 0, days/2)
			if err := fetch(from, middle); err != nil {
				return err
			}
			return fetch(middle, to)
		}
		if len(result.Rows)+len(rows) > usageQueryTotalLimit {
			return errors.New("The export exceeds 50,000 rows. Narrow the date range or filters.")
		}
		// Bound the final JSON, including escaping expansion, before responding.
		encodedRows, _ := json.Marshal(rows)
		bytesUsed += len(encodedRows)
		if bytesUsed > usageQueryBytesLimit-16384 {
			return errors.New("The export is too large. Narrow the date range or filters.")
		}
		result.Rows = append(result.Rows, rows...)
		return nil
	}
	// Short windows preserve daily precision on older date ranges. Split again
	// when the API reports a coarser bucket or hits its unpaginated row cap.
	for from := start; from.Before(end); {
		to := from.AddDate(0, 0, 7)
		if to.After(end) {
			to = end
		}
		if err := fetch(from, to); err != nil {
			return nil, err
		}
		from = to
	}
	sort.SliceStable(result.Rows, func(i, j int) bool {
		for col := 0; col <= len(q.GroupBy); col++ {
			if result.Rows[i][col] != result.Rows[j][col] {
				return result.Rows[i][col] < result.Rows[j][col]
			}
		}
		return false
	})
	result.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

func (a *app) usageQueryAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		jsonError(w, 405, "Use POST to query usage.")
		return
	}
	var q usageQuery
	if !decodeBody(w, r, &q) {
		return
	}
	if _, _, err := q.validate(time.Now()); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if !a.usageQueryMu.TryLock() {
		jsonError(w, 409, "A usage query is already running. Wait for it to finish.")
		return
	}
	defer a.usageQueryMu.Unlock()
	a.mu.Lock()
	key, org, endpoint, transport := a.apiKey, a.config.OrgID, a.accountURL, a.transport
	a.mu.Unlock()
	if key == "" {
		jsonError(w, 401, "Sign in to Kilo to retrieve historical usage.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	result, err := fetchUsageQuery(ctx, endpoint, transport, key, org, q, time.Now())
	a.mu.Lock()
	same := key == a.apiKey && org == a.config.OrgID && endpoint == a.accountURL
	a.mu.Unlock()
	if !same {
		jsonError(w, 409, "The Kilo account changed. Run the query again.")
		return
	}
	if err != nil {
		jsonError(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, result)
}
