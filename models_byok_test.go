package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogByokMetadataPreservesExplicitValues(t *testing.T) {
	models, err := parseModels([]byte(`{"data":[
		{"id":"vendor/true","hasUserByokAvailable":true},
		{"id":"vendor/false","hasUserByokAvailable":false},
		{"id":"vendor/missing","pricing":{"prompt":"0","completion":"0"}},
		{"id":"vendor/null","hasUserByokAvailable":null}
	]}`))
	if err != nil || len(models) != 4 {
		t.Fatalf("models=%v error=%v", models, err)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	byID := map[string]modelInfo{}
	wire := map[string]map[string]json.RawMessage{}
	for i, model := range models {
		byID[model.ID], wire[model.ID] = model, decoded[i]
	}
	for _, tc := range []struct {
		id, wantJSON string
		known, value bool
	}{
		{"vendor/true", "true", true, true},
		{"vendor/false", "false", true, false},
		{"vendor/missing", "null", false, false},
		{"vendor/null", "null", false, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			flag := byID[tc.id].HasUserByokAvailable
			if (flag != nil) != tc.known || flag != nil && *flag != tc.value {
				t.Fatalf("explicit BYOK value changed: %v", flag)
			}
			if got := string(wire[tc.id]["hasUserByokAvailable"]); got != tc.wantJSON {
				t.Fatalf("local JSON=%q, want %q", got, tc.wantJSON)
			}
		})
	}
	if byID["vendor/missing"].HasUserByokAvailable != nil {
		t.Fatal("BYOK availability inferred from provider or price")
	}
}

func TestCatalogByokOrganizationRefreshUsesCurrentContext(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "synthetic-byok-key", "org-true"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/gateway/models" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-byok-key" {
			t.Error("catalog request lost its authenticated context")
		}
		switch r.Header.Get("X-KiloCode-OrganizationId") {
		case "org-true":
			io.WriteString(w, `{"data":[{"id":"vendor/same","hasUserByokAvailable":true}]}`)
		case "org-false":
			io.WriteString(w, `{"data":[{"id":"vendor/same","hasUserByokAvailable":false}]}`)
		case "org-missing":
			io.WriteString(w, `{"data":[{"id":"vendor/same"}]}`)
		default:
			t.Error("catalog used an unexpected organization")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	for _, tc := range []struct {
		org          string
		known, value bool
	}{{"org-true", true, true}, {"org-false", true, false}, {"org-missing", false, false}} {
		a.mu.Lock()
		a.config.OrgID = tc.org
		a.catalogRevision++
		revision := a.catalogRevision
		a.mu.Unlock()
		w := adminRequest(a, "models", `{}`)
		if w.Code != http.StatusOK {
			t.Fatalf("organization %s status=%d body=%s", tc.org, w.Code, w.Body.String())
		}
		var response struct {
			Models   []modelInfo `json:"models"`
			Revision uint64      `json:"revision"`
			Complete bool        `json:"complete"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Revision != revision || !response.Complete || len(response.Models) != 1 || response.Models[0].ID != "vendor/same" || response.Models[0].Connection != "kilo" {
			t.Fatalf("wrong catalog context: %+v", response)
		}
		flag := response.Models[0].HasUserByokAvailable
		if (flag != nil) != tc.known || flag != nil && *flag != tc.value {
			t.Fatalf("old organization availability retained for %s: %v", tc.org, flag)
		}
		for _, private := range []string{"synthetic-byok-key", "org-true", "org-false", "org-missing"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private request context leaked into catalog")
			}
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("catalog was not refreshed for each context: calls=%d", calls.Load())
	}
}

func TestCatalogByokRejectsInFlightPreviousOrganization(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "synthetic-byok-key", "org-old"
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-byok-key" {
			t.Error("catalog credentials lost")
		}
		switch r.Header.Get("X-KiloCode-OrganizationId") {
		case "org-old":
			close(started)
			<-release
			io.WriteString(w, `{"data":[{"id":"vendor/same","hasUserByokAvailable":true}]}`)
		case "org-new":
			io.WriteString(w, `{"data":[{"id":"vendor/same","hasUserByokAvailable":false}]}`)
		default:
			t.Error("unexpected organization")
		}
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	type result struct {
		models []modelInfo
		err    error
	}
	done := make(chan result, 1)
	go func() {
		models, _, err := a.fetchKiloModels(context.Background(), true)
		done <- result{models, err}
	}()
	<-started
	a.mu.Lock()
	a.config.OrgID = "org-new"
	a.catalogRevision++
	a.mu.Unlock()
	close(release)
	stale := <-done
	var problem *catalogError
	if len(stale.models) != 0 || !errors.As(stale.err, &problem) || problem.status != http.StatusConflict {
		t.Fatalf("accepted old organization BYOK catalog: %+v", stale)
	}
	models, _, err := a.fetchKiloModels(context.Background(), true)
	if err != nil || len(models) != 1 || models[0].HasUserByokAvailable == nil || *models[0].HasUserByokAvailable {
		t.Fatalf("new organization catalog=%+v error=%v", models, err)
	}
}

func TestCatalogByokDoesNotLabelNativeChatGPTModels(t *testing.T) {
	c, _ := chatGPTTestConnection(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/models" {
			t.Error("unexpected native catalog route")
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-fixture","display_name":"Fixture","hasUserByokAvailable":true}]}`)
	}, true)
	c.mu.Lock()
	c.creds.Expires = time.Now().Add(time.Hour).Unix()
	c.mu.Unlock()
	models, err := c.models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "chatgpt/gpt-fixture" || models[0].HasUserByokAvailable != nil {
		t.Fatalf("Kilo-specific availability mixed into native catalog: %+v %v", models, err)
	}
}
