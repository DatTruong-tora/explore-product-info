package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

const (
	defaultRelatedPatentLimit = 10
	maxRelatedPatentLimit     = 50
)

func FindRelatedPatentIDs(inventionText string, limit int) (*models.RelatedPatentsResponse, error) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	response := &models.RelatedPatentsResponse{
		InventionText: trimmedText,
		Limit:         limit,
		PatentIDs:     make([]string, 0, limit),
	}

	seen := make(map[string]struct{}, limit)
	for _, query := range buildPatentSearchQueries(trimmedText) {
		patentData, err := fetchPatentDataBySearchText(ctx, query)
		if err != nil {
			return nil, err
		}

		for _, patentItem := range patentData.PatentFileWrapperDataBag {
			patentID := strings.TrimSpace(patentItem.ApplicationNumberText)
			if patentID == "" {
				continue
			}
			if _, ok := seen[patentID]; ok {
				continue
			}

			seen[patentID] = struct{}{}
			response.PatentIDs = append(response.PatentIDs, patentID)
			if len(response.PatentIDs) >= limit {
				return response, nil
			}
		}
	}

	return response, nil
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
