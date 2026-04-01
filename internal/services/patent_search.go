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
)

// FindRelatedPatentIDs resolves related patent identifiers from USPTO (when USPTO_API_KEY is set)
// and SerpAPI Google Patents (when SERP_API_KEY or SERPAPI_API_KEY is set). Sources run concurrently;
// if one fails and the other succeeds, results from the successful source are returned.
func FindRelatedPatentIDs(ctx context.Context, inventionText string, limit int) (*models.RelatedPatentsResponse, error) {
	trimmedText := strings.TrimSpace(inventionText)
	if trimmedText == "" {
		return nil, fmt.Errorf("invention text is required")
	}

	if limit <= 0 {
		limit = defaultRelatedPatentLimit
	}
	if limit > maxRelatedPatentLimit {
		limit = maxRelatedPatentLimit
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	usptoConfigured := strings.TrimSpace(os.Getenv("USPTO_API_KEY")) != ""
	serpKey := SerpAPIKey()
	serpConfigured := serpKey != ""

	if !usptoConfigured && !serpConfigured {
		return nil, fmt.Errorf("no patent search configured: set USPTO_API_KEY and/or SERP_API_KEY (or SERPAPI_API_KEY)")
	}

	var wg sync.WaitGroup
	var usptoIDs []string
	var usptoErr error
	var serpIDs []string
	var serpErr error

	if usptoConfigured {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usptoIDs, usptoErr = collectUSPTOPatentIDs(ctx, trimmedText, limit)
		}()
	}

	if serpConfigured {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serpIDs, serpErr = fetchSerpAPIRelatedPatentIDs(ctx, serpKey, trimmedText, limit)
		}()
	}

	wg.Wait()

	if err := relatedPatentsSourceFailure(usptoConfigured, serpConfigured, usptoErr, serpErr); err != nil {
		return nil, err
	}
	if usptoErr != nil {
		log.Printf("related patent search partial failure: USPTO source failed: %v", usptoErr)
	}
	if serpErr != nil {
		log.Printf("related patent search partial failure: SerpAPI source failed: %v", serpErr)
	}

	merged := mergeRelatedPatentIDs(usptoIDs, serpIDs, limit)

	return &models.RelatedPatentsResponse{
		InventionText: trimmedText,
		Limit:         limit,
		PatentIDs:     merged,
	}, nil
}

func relatedPatentsSourceFailure(usptoConfigured, serpConfigured bool, usptoErr, serpErr error) error {
	switch {
	case usptoConfigured && !serpConfigured:
		return usptoErr
	case !usptoConfigured && serpConfigured:
		return serpErr
	default:
		if usptoErr != nil && serpErr != nil {
			return fmt.Errorf("patent search failed: USPTO: %v; SerpAPI: %v", usptoErr, serpErr)
		}
		return nil
	}
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

func mergeRelatedPatentIDs(uspto, serp []string, limit int) []string {
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
	for _, id := range uspto {
		if len(out) >= limit {
			return out
		}
		add(id)
	}
	for _, id := range serp {
		if len(out) >= limit {
			break
		}
		add(id)
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
