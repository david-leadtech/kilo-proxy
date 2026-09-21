//go:build desktop

package main

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeBillingMoneyDoesNotShowMissingOrStaleAsZero(t *testing.T) {
	zero, negative, small := "0.000000000", "-0.062515000", "0.000000001"
	for _, tc := range []struct {
		value  *string
		status string
		stale  bool
		want   string
	}{
		{&zero, "ready", false, "$0"}, {&small, "ready", false, "$0.000000001"}, {&negative, "ready", false, "$-0.062515"},
		{nil, "ready", false, "—"}, {&zero, "ready", true, "—"}, {&zero, "unavailable", false, "—"}, {&zero, "loading", false, "—"},
	} {
		if got := nativeBillingMoney(tc.value, tc.status, tc.stale); got != tc.want {
			t.Fatalf("%+v: %q", tc, got)
		}
	}
}

func TestNativeAccountUsageLayoutAndDailyDetails(t *testing.T) {
	for _, size := range []image.Point{{1180, 820}, {780, 700}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page, u.language = "activity", lang
				u.state["billing"] = map[string]any{
					"status": "ready", "scope": "organization", "balanceUSD": "248.623145000", "fetchedAt": "2026-09-21T09:15:00Z",
					"usage": map[string]any{"status": "ready", "scope": "self", "todayUSD": "1.203456000", "yesterdayUSD": "8.956780000", "last30DaysUSD": "72.068192000", "fetchedAt": "2026-09-21T09:15:00Z", "days": []map[string]any{{"date": "2026-09-20", "costUSD": "8.956780000", "requests": 124, "input": 53012, "output": 600, "cached": 44000}}},
				}
				u.state["usageHistory"] = map[string]any{"startedAt": "2026-09-19T17:20:00Z", "timezone": "Europe/Madrid", "today": usageSummary{Requests: 5, Priced: 3, CostUSD: "0.345678000"}, "yesterday": usageSummary{Requests: 4, Priced: 4, CostUSD: "0.854321000"}, "last7Days": usageSummary{Requests: 9, Priced: 7, CostUSD: "1.199999000"}}
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				h.target(u.tr("Refresh balance and usage", "Actualizar saldo y uso"), semantic.Button)
				nativeGridCapture(t, h, "account-usage-"+fmtSize(size)+"-"+lang)
				for _, amount := range []string{"$248.623145", "$1.203456", "$8.95678"} {
					found := false
					for _, node := range h.nodes() {
						if node.Desc.Label == amount {
							found = true
						}
					}
					if !found {
						t.Fatalf("account metric %q absent from layout", amount)
					}
				}
				// The expander must expose the authoritative daily records while
				// keeping the daily local inference totals in a separate section.
				nativeMenuWheel(h, image.Pt(size.X-100, size.Y-140), 330)
				h.click(u.tr("Daily Kilo usage", "Uso diario en Kilo"), semantic.Button)
				if !u.checked("billing.daily.open") {
					t.Fatal("daily history did not open")
				}
				found := false
				for _, node := range h.nodes() {
					if strings.Contains(node.Desc.Label, "2026-09-20 · $8.95678") {
						found = true
					}
				}
				if !found {
					t.Fatal("daily Kilo usage row missing")
				}
				nativeGridCapture(t, h, "account-usage-daily-"+fmtSize(size)+"-"+lang)
			})
		}
	}
}
