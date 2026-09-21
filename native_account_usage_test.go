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
				u.page = "activity"
				// Seed the source served by /api/state, not just the rendered
				// copy. Real one-second refreshes can run during a slow GPU
				// screenshot and must preserve the synthetic account and locale.
				now := time.Now()
				balance, todayCost, yesterdayCost, monthCost := "248.623145000", "1.203456000", "8.956780000", "72.068192000"
				remoteDate := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
				u.owner.mu.Lock()
				u.owner.config.Language = lang
				u.owner.syncBillingIdentityLocked()
				u.owner.billing.snapshot = billingSnapshot{
					Status: "ready", Scope: "organization", OrgID: u.owner.config.OrgID, BalanceUSD: &balance, FetchedAt: now.Format(time.RFC3339),
					Usage: accountUsageSnapshot{Status: "ready", Scope: "self", TimeZone: "UTC", TodayUSD: &todayCost, YesterdayUSD: &yesterdayCost, Last30DaysUSD: &monthCost, FetchedAt: now.Format(time.RFC3339), PeriodEnd: now.UTC().Format(time.RFC3339), Days: []accountUsageDay{{Date: remoteDate, CostUSD: yesterdayCost, Requests: 124, Input: 53012, Output: 600, Cached: 44000}}},
				}
				u.owner.usageHistory.mu.Lock()
				u.owner.usageHistory.disk.Scopes[usageAccountID(u.owner.apiKey, u.owner.config.OrgID)] = usageHistoryScope{
					StartedAt: now.AddDate(0, 0, -2).Format(time.RFC3339),
					Days: []usageHistoryDay{
						{Date: now.Format("2006-01-02"), usageSummary: usageSummary{Requests: 5, Priced: 3, CostUSD: "0.345678000", costNanos: 345678000}},
						{Date: now.AddDate(0, 0, -1).Format("2006-01-02"), usageSummary: usageSummary{Requests: 4, Priced: 4, CostUSD: "0.854321000", costNanos: 854321000}},
					},
				}
				u.owner.usageHistory.mu.Unlock()
				u.owner.mu.Unlock()
				u.refreshState()
				nativeTestWait(t, u, func() bool {
					return !u.busy["GET/api/state"] && nativeString(nativeMap(u.state["billing"]), "status") == "ready" && u.language == lang
				})
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				h.target(u.tr("Refresh balance and usage", "Actualizar saldo y uso"), semantic.Button)
				nativeGridCapture(t, h, "account-usage-"+fmtSize(size)+"-"+lang)
				// Exercise the same refresh that used to erase the fixture on
				// slower CI runners before using the daily-history controls.
				u.refreshState()
				nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
				h.frame()
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
					if strings.Contains(node.Desc.Label, remoteDate+" · $8.95678") {
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
