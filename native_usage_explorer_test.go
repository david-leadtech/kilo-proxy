//go:build desktop

package main

import (
	"image"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeUsageCSVFilesAtSameTimePreserveRawData(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 15, 12, 34, 56, 789, time.FixedZone("selected", 2*60*60))
	result := &usageQueryResult{
		Query:   usageQuery{StartDate: "2025-01-01", EndDate: "2025-01-01", CostSource: "cost"},
		Columns: []string{"datetime", "project", "costMicrodollars", "inputTokens"},
		Rows:    [][]string{{"2025-01-01", "bare\rcarriage,\"quoted\"\r\nline", "123.000125", "9007199254740993"}},
	}
	wantFirst := "datetime,project,costMicrodollars,inputTokens\n2025-01-01,\"bare\rcarriage,\"\"quoted\"\"\r\nline\",123.000125,9007199254740993\n"
	first, err := writeUsageCSVFileAt(dir, result, now)
	if err != nil {
		t.Fatal("first fixed-time export failed", err)
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != wantFirst {
		t.Fatalf("first export changed raw CSV: got %q, err %v", data, err)
	}
	result.Rows = [][]string{{"2025-01-01", "second", "1e-6", "2"}}
	wantSecond := "datetime,project,costMicrodollars,inputTokens\n2025-01-01,second,1e-6,2\n"
	second, err := writeUsageCSVFileAt(dir, result, now)
	if err != nil {
		t.Fatal("second export at the identical time failed", err)
	}
	if first == second {
		t.Fatal("fixed-time exports reused the same path")
	}
	for path, want := range map[string]string{first: wantFirst, second: wantSecond} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("export %s changed raw CSV or overwrote an earlier export: got %q, err %v", path, data, err)
		}
		prefix := strings.TrimSuffix(usageQueryFilename(result.Query), ".csv") + "-" + now.UTC().Format("20060102T150405.000000000") + "-"
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), prefix) || filepath.Ext(path) != ".csv" {
			t.Fatalf("export lost its destination, query or UTC timestamp: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("export is not a regular file: %s, %v", path, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("export permissions are %o, want 0600", info.Mode().Perm())
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("identical-time exports did not leave exactly two files: %d, %v", len(files), err)
	}
}

func TestNativeUsageCSVFilesRejectInvalidDestinations(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-folder")
	if err := os.WriteFile(file, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"", "relative", filepath.Join(dir, "missing"), file} {
		if path, err := writeUsageCSVFileAt(destination, &usageQueryResult{}, time.Time{}); err == nil || path != "" {
			t.Fatalf("invalid destination %q returned %q, %v", destination, path, err)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "existing" {
		t.Fatalf("invalid destination overwrote an existing file: %q, %v", data, err)
	}
}

func TestNativeUsageExplorerQueriesAndExportsExactCSV(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "activity"
	u.uxa().tab = "history"
	u.usageExplorerState()
	u.setValue("usage.start", "2025-01-01")
	u.setValue("usage.end", "2025-01-01")
	u.owner.mu.Lock()
	u.owner.transport = usageMemoryTransport(func(r *http.Request) (*http.Response, error) {
		return billingTestResponse(200, billingTestUsage(usageQueryTestRow("2025-01-01", map[string]string{"model": "vendor/one"}))), nil
	})
	u.owner.mu.Unlock()
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1000), now: time.Now()}
	h.frame()
	h.click("Retrieve history", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["POST/api/usage/query"] })
	if u.usageExplorer.result == nil || len(u.usageExplorer.result.Rows) != 1 {
		t.Fatalf("historical query failed: %s", u.notice)
	}
	dir := t.TempDir()
	if _, err := writeUsageCSVFile("", u.usageExplorer.result); err == nil {
		t.Fatal("empty folder silently used the working directory")
	}
	u.setValue("usage.folder", dir)
	u.saveUsageHistoryCSV()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("CSV was not saved", err, u.notice)
	}
	data, err := os.ReadFile(dir + string(os.PathSeparator) + files[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "123.000125,2,9007199254740993,20,10,60,0") {
		t.Fatalf("raw CSV precision lost: %s", data)
	}
	u.saveUsageHistoryCSV()
	files, _ = os.ReadDir(dir)
	if len(files) != 2 {
		t.Fatal("second export overwrote the first")
	}
	u.owner.mu.Lock()
	u.owner.config.OrgID = "other-team"
	u.owner.mu.Unlock()
	u.saveUsageHistoryCSV()
	files, _ = os.ReadDir(dir)
	if len(files) != 2 || u.usageExplorer.result != nil {
		t.Fatal("export used stale account rows")
	}
}

func TestNativeUsageExplorerLayoutAndGroupingLimits(t *testing.T) {
	for _, size := range []image.Point{{1180, 1000}, {780, 1000}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page = "activity"
				u.language = lang
				u.uxa().tab = "history"
				u.usageExplorerState()
				u.setChecked("usage.group.provider", true)
				u.setChecked("usage.group.project", true)
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				nativeGridCapture(t, h, "usage-explorer-"+fmtSize(size)+"-"+lang)
				if len(u.usageExplorerQuery().GroupBy) != 3 {
					t.Fatal("selected cross lost")
				}
				active := 0
				for _, node := range h.nodes() {
					if node.Desc.Class == semantic.CheckBox {
						active++
					}
				}
				if active != 4 {
					t.Fatalf("expected cost basis and three active grouping controls, got %d", active)
				}
				h.click(u.tr("Provider", "Proveedor"), semantic.CheckBox)
				h.click(u.tr("User", "Usuario"), semantic.CheckBox)
				if len(u.usageExplorerQuery().GroupBy) != 3 || !u.checked("usage.group.user") {
					t.Fatal("grouping controls did not re-enable after removing a dimension")
				}
			})
		}
	}
}
