package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

const (
	defaultRelatedPatentLimit = 10
	maxRelatedPatentLimit     = 1000

	patentProviderUSPTO   = "uspto"
	patentProviderSerpAPI = "serpapi"
)

func usptoAPIKeyConfigured() bool {
	return strings.Trim(strings.TrimSpace(os.Getenv("USPTO_API_KEY")), `"'`) != ""
}

// PatentProvider is a registered patent ID source. Add new backends by appending
// to activePatentProviders(); core orchestration stays in executePatentProviderSearch.
type PatentProvider struct {
	Name  string
	Fetch func(ctx context.Context, inventionText string, limit int) ([]string, error)
}

// activePatentProviders returns providers enabled by environment variables.
func activePatentProviders() []PatentProvider {
	var out []PatentProvider
	if usptoAPIKeyConfigured() {
		out = append(out, PatentProvider{
			Name: patentProviderUSPTO,
			Fetch: func(ctx context.Context, inventionText string, limit int) ([]string, error) {
				return collectUSPTOPatentIDs(ctx, inventionText, limit)
			},
		})
	}
	serpKey := SerpAPIKey()
	if strings.TrimSpace(serpKey) != "" {
		key := serpKey
		out = append(out, PatentProvider{
			Name: patentProviderSerpAPI,
			Fetch: func(ctx context.Context, inventionText string, limit int) ([]string, error) {
				return fetchSerpAPIRelatedPatentIDs(ctx, key, inventionText, limit)
			},
		})
	}
	return out
}

// FindRelatedPatentIDs resolves related patent identifiers from enabled providers
// (USPTO when USPTO_API_KEY is set; SerpAPI Google Patents when SERP_API_KEY or
// SERPAPI_API_KEY is set). Providers run concurrently; partial failures are logged
// and ignored if at least one provider succeeds.
func FindRelatedPatentIDs(ctx context.Context, inventionText string, limit int) (*models.RelatedPatentsResponse, error) {
	inventionText = cleanPatentInventionNoise(inventionText)
	if inventionText == "" {
		return nil, fmt.Errorf("invention text is required")
	}

	if limit <= 0 {
		limit = defaultRelatedPatentLimit
	}
	if limit > maxRelatedPatentLimit {
		limit = maxRelatedPatentLimit
	}

	providers := activePatentProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no patent search configured: set USPTO_API_KEY and/or SERP_API_KEY (or SERPAPI_API_KEY)")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	return executePatentProviderSearch(ctx, inventionText, limit, providers)
}

// executePatentProviderSearch runs all providers concurrently and merges results.
// Exposed to tests in this package via same-name calls with injected providers.
func executePatentProviderSearch(ctx context.Context, inventionText string, limit int, providers []PatentProvider) (*models.RelatedPatentsResponse, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("no patent search configured: set USPTO_API_KEY and/or SERP_API_KEY (or SERPAPI_API_KEY)")
	}

	type outcome struct {
		name string
		ids  []string
		err  error
	}
	outcomes := make([]outcome, len(providers))

	var wg sync.WaitGroup
	for i := range providers {
		i := i
		p := providers[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := p.Fetch(ctx, inventionText, limit)
			outcomes[i] = outcome{name: p.Name, ids: ids, err: err}
		}()
	}
	wg.Wait()

	var failMsgs []string
	var successSlices [][]string
	for _, o := range outcomes {
		if o.err != nil {
			log.Printf("related patent search partial failure: provider %q failed: %v", o.name, o.err)
			failMsgs = append(failMsgs, fmt.Sprintf("%s: %v", o.name, o.err))
			continue
		}
		successSlices = append(successSlices, o.ids)
	}

	if len(successSlices) == 0 {
		return nil, fmt.Errorf("patent search failed: all %d providers failed (%s)", len(providers), strings.Join(failMsgs, "; "))
	}

	merged := mergeRelatedPatentIDs(limit, successSlices...)

	return &models.RelatedPatentsResponse{
		InventionText: inventionText,
		Limit:         limit,
		PatentIDs:     merged,
	}, nil
}

func collectUSPTOPatentIDs(ctx context.Context, inventionText string, limit int) ([]string, error) {
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)
	var lastErr error
	anySuccess := false

	for _, query := range buildPatentSearchQueries(inventionText) {
		if len(out) >= limit {
			break
		}
		patentData, err := fetchPatentDataBySearchText(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		anySuccess = true
		for _, patentItem := range patentData.PatentFileWrapperDataBag {
			patentID := strings.TrimSpace(patentItem.ApplicationNumberText)
			if patentID == "" {
				continue
			}
			key := normalizePatentIDKey(patentID)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, patentID)
			log.Printf("USPTO patent ID: %s", patentID)
			if len(out) >= limit {
				log.Printf("USPTO patent IDs: %v", out)
				return out, nil
			}
		}
	}

	if !anySuccess && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// mergeRelatedPatentIDs interleaves patent IDs from N sources in round-robin order
// (fair exposure of top ranks across providers), then dedupes using normalizePatentIDKey.
func mergeRelatedPatentIDs(limit int, sources ...[]string) []string {
	if limit <= 0 || len(sources) == 0 {
		return nil
	}

	maxRound := 0
	for _, s := range sources {
		if n := len(s); n > maxRound {
			maxRound = n
		}
	}

	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)

	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		key := normalizePatentIDKey(raw)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}

	for round := 0; round < maxRound && len(out) < limit; round++ {
		for si := 0; si < len(sources) && len(out) < limit; si++ {
			if round >= len(sources[si]) {
				continue
			}
			add(sources[si][round])
		}
	}

	return out
}

// normalizePatentIDKey returns a canonical form for merge/dedupe only.
// It uppercases, trims, then keeps ASCII letters and digits so equivalent
// surface forms (spaces, commas, slashes, hyphens) map to one key.
// Outward API responses still use each source’s raw patent_ids string.
func normalizePatentIDKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToUpper(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func buildPatentSearchQueries(inventionText string) []string {
	queries := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)

	add := func(query string) {
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		if _, ok := seen[query]; ok {
			return
		}
		seen[query] = struct{}{}
		queries = append(queries, query)
	}

	add(fmt.Sprintf(`inventionTitle:"%s"`, inventionText))
	add(fmt.Sprintf(`"%s"`, inventionText))

	keyTerms := extractPatentSearchTerms(inventionText)
	if len(keyTerms) > 0 {
		add(strings.Join(keyTerms, " AND "))
	}

	return queries
}

func extractPatentSearchTerms(inventionText string) []string {
	stopwords := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "for": {}, "from": {}, "in": {}, "into": {}, "of": {},
		"on": {}, "or": {}, "the": {}, "to": {}, "with": {}, "without": {}, "using": {},
	}

	normalized := strings.NewReplacer(",", " ", ".", " ", ";", " ", ":", " ", "-", " ", "_", " ", "/", " ").Replace(strings.ToLower(inventionText))
	terms := strings.Fields(normalized)

	result := make([]string, 0, 5)
	seen := make(map[string]struct{}, 5)
	for _, term := range terms {
		if len(term) < 3 {
			continue
		}
		if _, ok := stopwords[term]; ok {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
		if len(result) == 5 {
			break
		}
	}

	return result
}
