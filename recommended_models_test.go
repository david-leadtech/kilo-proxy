package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The published file is also the compiled fallback; a broken edit must fail CI.
func TestPublishedRecommendedModelsAreValid(t *testing.T) {
	data, err := parseRecommendedModels(embeddedRecommendedModels)
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for id, r := range data {
		if r.Default {
			defaults++
		}
		if r.NoteEN == "" || r.NoteES == "" {
			t.Errorf("%s needs English and Spanish notes", id)
		}
	}
	if defaults != 1 {
		t.Fatal("published list needs one default")
	}
}

func TestRecommendedModelsRejectInvalidLists(t *testing.T) {
	for name, body := range map[string]string{
		"schema":        `{"schemaVersion":2,"models":[{"id":"a/b","default":true}]}`,
		"empty":         `{"schemaVersion":1,"models":[]}`,
		"no default":    `{"schemaVersion":1,"models":[{"id":"a/b"}]}`,
		"two defaults":  `{"schemaVersion":1,"models":[{"id":"a/b","default":true},{"id":"a/c","default":true}]}`,
		"duplicate":     `{"schemaVersion":1,"models":[{"id":"a/b","default":true},{"id":"a/b"}]}`,
		"invalid id":    `{"schemaVersion":1,"models":[{"id":"../../etc","default":true}]}`,
		"control chars": `{"schemaVersion":1,"models":[{"id":"a/b","default":true,"note":{"en":"x\u001b[31m"}}]}`,
		"long note":     `{"schemaVersion":1,"models":[{"id":"a/b","default":true,"note":{"es":"` + strings.Repeat("x", recommendedNoteMax+1) + `"}}]}`,
		"trailing":      `{"schemaVersion":1,"models":[{"id":"a/b","default":true}]} {}`,
		"reasoning":     `{"schemaVersion":1,"models":[{"id":"a/b","default":true,"reasoning":"turbo"}]}`,
	} {
		if _, err := parseRecommendedModels([]byte(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRecommendedModelsMarkOnlyCatalogModels(t *testing.T) {
	const remote = `{"schemaVersion":1,"models":[{"id":"vendor/missing"},{"id":"vendor/two","default":true,"note":{"en":"Remote"}},{"id":"vendor/one"}]}`
	for _, mode := range []string{"remote", "offline", "invalid", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			a.modelStatsURL = "https://metadata.invalid/stats"
			a.recommendedModelsURL = "https://list.invalid/recommended-models.json"
			a.apiKey, a.config.OrgID = "private-personal-key", "private-team"
			var listCalls atomic.Int32
			a.transport = modelMetricTransport(func(r *http.Request) (*http.Response, error) {
				status, body, header := http.StatusOK, `{"data":[{"id":"vendor/one"},{"id":"vendor/two"},{"id":"openai/gpt-6-luna"}]}`, make(http.Header)
				switch r.URL.Host {
				case "list.invalid":
					listCalls.Add(1)
					if r.Header.Get("Authorization") != "" || r.Header.Get("X-KiloCode-OrganizationId") != "" || r.Header.Get("Cookie") != "" {
						t.Error("Kilo credentials reached the recommendations host")
					}
					switch mode {
					case "remote":
						body = remote
					case "offline":
						return nil, errors.New("offline")
					case "invalid":
						body = `{"schemaVersion":1,"models":[{"id":"vendor/one"}]}`
					case "redirect":
						status = http.StatusFound
						header.Set("Location", "https://must-not-follow.invalid/")
					}
				case "metadata.invalid":
					status = http.StatusNotFound
				case "api.kilo.ai":
				default:
					t.Errorf("unexpected network destination %s", r.URL.Host)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			for range 2 {
				models, _, err := a.fetchModels(context.Background(), false)
				if err != nil || len(models) != 3 {
					t.Fatalf("catalog changed: %v %v", models, err)
				}
				marked := map[string]*modelRecommendation{}
				for _, m := range models {
					marked[m.ID] = m.Recommendation
				}
				if mode == "remote" {
					if marked["vendor/two"] == nil || !marked["vendor/two"].Default || marked["vendor/two"].Rank != 2 || marked["vendor/two"].NoteEN != "Remote" || marked["vendor/one"] == nil || marked["openai/gpt-6-luna"] != nil {
						t.Fatalf("remote list not applied: %+v", marked)
					}
				} else if marked["vendor/one"] != nil || marked["openai/gpt-6-luna"] == nil || !marked["openai/gpt-6-luna"].Default || marked["openai/gpt-6-luna"].Reasoning != "max" {
					// Failed or invalid remote lists fall back to the compiled copy.
					t.Fatalf("compiled fallback not applied: %+v", marked)
				}
			}
			if listCalls.Load() != 1 {
				t.Fatal("recommendations were not cached")
			}
		})
	}
}
