//go:build desktop

package main

import (
	"image"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

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
