package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The published list lives in the repository so it can change without a
// release. The copy compiled into the binary is used until a valid remote list
// arrives and whenever GitHub is unreachable.
//
//go:embed recommended-models.json
var embeddedRecommendedModels []byte

const (
	recommendedModelsEndpoint = "https://raw.githubusercontent.com/rosseca/kilo-proxy/main/recommended-models.json"
	recommendedModelsTimeout  = 3 * time.Second
	recommendedModelsLimit    = 64 << 10
	recommendedModelsMax      = 50
	recommendedNoteMax        = 160
)

// A recommendation only marks a model the organization catalog already
// returned. It never adds models, prices or endpoints; its only client setting
// is an initial reasoning level, applied when the catalog lists that level.
type modelRecommendation struct {
	Rank      int    `json:"rank"`
	Default   bool   `json:"default,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	NoteEN    string `json:"noteEn,omitempty"`
	NoteES    string `json:"noteEs,omitempty"`
}

// Protected by app.mu. Maps become immutable once published to callers.
type recommendedModelsCache struct {
	endpoint  string
	expiresAt time.Time
	data      map[string]modelRecommendation
}

func parseRecommendedModels(body []byte) (map[string]modelRecommendation, error) {
	var file struct {
		SchemaVersion int `json:"schemaVersion"`
		Models        []struct {
			ID        string `json:"id"`
			Default   bool   `json:"default"`
			Reasoning string `json:"reasoning"`
			Note      struct {
				EN string `json:"en"`
				ES string `json:"es"`
			} `json:"note"`
		} `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing data after recommended models")
	}
	if file.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported recommended models schema %d", file.SchemaVersion)
	}
	if len(file.Models) == 0 || len(file.Models) > recommendedModelsMax {
		return nil, errors.New("recommended models list is empty or too long")
	}
	result := make(map[string]modelRecommendation, len(file.Models))
	defaults := 0
	for i, m := range file.Models {
		if !catalogID.MatchString(m.ID) {
			return nil, fmt.Errorf("invalid recommended model ID %q", m.ID)
		}
		if _, duplicate := result[m.ID]; duplicate {
			return nil, fmt.Errorf("duplicate recommended model %q", m.ID)
		}
		for _, note := range []string{m.Note.EN, m.Note.ES} {
			if !validRecommendationNote(note) {
				return nil, fmt.Errorf("invalid note for recommended model %q", m.ID)
			}
		}
		if m.Reasoning != "" && !effortNames[m.Reasoning] {
			return nil, fmt.Errorf("invalid reasoning for recommended model %q", m.ID)
		}
		if m.Default {
			defaults++
		}
		result[m.ID] = modelRecommendation{Rank: i + 1, Default: m.Default, Reasoning: m.Reasoning, NoteEN: m.Note.EN, NoteES: m.Note.ES}
	}
	if defaults != 1 {
		return nil, errors.New("recommended models need exactly one default")
	}
	return result, nil
}

func validRecommendationNote(note string) bool {
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > recommendedNoteMax {
		return false
	}
	return strings.IndexFunc(note, unicode.IsControl) < 0
}

func embeddedModelRecommendations() map[string]modelRecommendation {
	data, err := parseRecommendedModels(embeddedRecommendedModels)
	if err != nil {
		// The embedded file is validated by tests; a broken build shows no recommendations.
		return nil
	}
	return data
}

func (a *app) modelRecommendations(ctx context.Context) map[string]modelRecommendation {
	a.mu.Lock()
	endpoint := a.recommendedModelsURL
	if endpoint == "" && a.upstream.Scheme == "https" && a.upstream.Host == "api.kilo.ai" {
		endpoint = recommendedModelsEndpoint
	}
	// Synthetic/custom gateways use the compiled list and make no GitHub request.
	if endpoint == "" {
		a.mu.Unlock()
		return embeddedModelRecommendations()
	}
	cache := &a.recommendedModelsCache
	if cache.endpoint == endpoint && time.Now().Before(cache.expiresAt) {
		data := cache.data
		a.mu.Unlock()
		return data
	}
	var previous map[string]modelRecommendation
	if cache.endpoint == endpoint {
		previous = cache.data
	}
	transport := a.transport
	a.mu.Unlock()

	data, err := fetchRecommendedModels(ctx, endpoint, transport)
	ttl := time.Hour
	if err != nil {
		data, ttl = previous, 5*time.Minute
		if data == nil {
			data = embeddedModelRecommendations()
		}
	}
	a.mu.Lock()
	a.recommendedModelsCache = recommendedModelsCache{endpoint: endpoint, data: data, expiresAt: time.Now().Add(ttl)}
	a.mu.Unlock()
	return data
}

func fetchRecommendedModels(ctx context.Context, endpoint string, transport http.RoundTripper) (map[string]modelRecommendation, error) {
	ctx, cancel := context.WithTimeout(ctx, recommendedModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.User != nil {
		return nil, errors.New("recommended models URL must not contain credentials")
	}
	// Unauthenticated and independent of the gateway request: no Kilo key,
	// organization header, local key or cookies reach GitHub.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Kilo-Proxy/"+version)
	client := &http.Client{Transport: transport, Timeout: recommendedModelsTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("recommended models unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, recommendedModelsLimit+1))
	if err != nil || len(body) > recommendedModelsLimit {
		return nil, errors.New("invalid recommended models response")
	}
	return parseRecommendedModels(body)
}
