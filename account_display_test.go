package main

import (
	"strings"
	"testing"
	"time"
)

func TestAccountBalanceAmountPreservesSignsAndPrecision(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"0.000000000", "$0.00"}, {"0.000000001", "<$0.01"},
		{"-0.000000001", "−<$0.01"}, {"-0.062515000", "−$0.06"},
		{"1234.995", "$1235.00"}, {"-1234.995", "−$1235.00"},
		{"9007199254740993.015", "$9007199254740993.02"},
		{"NaN", "—"}, {"Infinity", "—"}, {"1/2", "—"}, {"1e3", "—"}, {"", "—"},
	} {
		if got := accountBalanceAmount(&tc.input); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.input, got, tc.want)
		}
	}
	if accountBalanceAmount(nil) != "—" {
		t.Fatal("missing balance became zero")
	}
}

func TestTrayBalancePreferencePersists(t *testing.T) {
	a := testApp(t)
	w := traySettingsRequest(a, "PUT", `{"display":"balance"}`, a.adminToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err := readSettings(a.dir)
	if err != nil || saved.TrayDisplay != trayDisplayBalance {
		t.Fatalf("balance preference lost: %+v %v", saved, err)
	}
	if got := a.trayState(); got.display != trayDisplayBalance || got.amount != "—" {
		t.Fatalf("missing account balance looked known: %+v", got)
	}
}

func TestTrayBalanceRejectsStaleAndCrossAccountAmounts(t *testing.T) {
	a := testApp(t)
	a.config.TrayDisplay, a.config.Language = trayDisplayBalance, "en"
	a.apiKey, a.config.OrgID = "synthetic-key", "team-one"
	a.syncBillingIdentityLocked()
	negative := "-0.062515000"
	a.billing.snapshot = billingSnapshot{Status: "ready", Scope: "organization", OrgID: "team-one", BalanceUSD: &negative, FetchedAt: time.Now().Format(time.RFC3339)}
	s := a.trayState()
	if s.amount != "−$0.06" || !strings.Contains(s.tooltip, "Team balance: −$0.06") {
		t.Fatalf("signed shared balance not shown: %+v", s)
	}
	a.billing.snapshot.Stale = true
	if s = a.trayState(); s.amount != "—" || !strings.Contains(s.tooltip, "out of date") {
		t.Fatalf("stale balance looked current: %+v", s)
	}
	a.billing.snapshot.Stale = false
	a.billing.snapshot.Status = "unavailable"
	if s = a.trayState(); s.amount != "—" || !strings.Contains(s.tooltip, "unavailable") {
		t.Fatalf("failed balance looked available: %+v", s)
	}
	a.billing.snapshot.Status = "ready"
	a.config.OrgID = "team-two"
	if s = a.trayState(); s.amount != "—" || strings.Contains(s.tooltip, "$0.06") {
		t.Fatalf("old team balance leaked: %+v", s)
	}
}
