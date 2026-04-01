package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// serpAPIPatentsSearchURL is the SerpAPI JSON search endpoint (tests may override).
var serpAPIPatentsSearchURL = "https://serpapi.com/search.json"

type serpPatentsSearchResponse struct {
	Error          string                  `json:"error"`
	OrganicResults []serpPatentsOrganicRow `json:"organic_results"`
}

type serpPatentsOrganicRow struct {
	IsScholar         bool   `json:"is_scholar"`
	ScholarID         string `json:"scholar_id"`
	PatentID          string `json:"patent_id"`
	PublicationNumber string `json:"publication_number"`
}

// buildSerpPatentQueries builds compact Google Patents–style queries from invention text.
// It uses extractPatentSearchTerms (no raw prose). Returns nil if no usable terms exist.
func buildSerpPatentQueries(inventionText string) []string {
	terms := extractPatentSearchTerms(inventionText)
	if len(terms) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, 8)
	queries := make([]string, 0, 4)
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		if _, ok := seen[q]; ok {
			return
		}
		seen[q] = struct{}{}
		queries = append(queries, q)
	}

	orGroup := func(ts []string) string {
		parts := make([]string, len(ts))
		for i, t := range ts {
			parts[i] = "(" + t + ")"
		}
		return strings.Join(parts, " OR ")
	}

	add(orGroup(terms))
	add(strings.Join(terms, "; "))
	if len(terms) >= 3 {
		add(orGroup(terms[:3]))
	}
	if len(terms) >= 2 {
		add(orGroup(terms[:2]))
	}

	return queries
}

// isSerpPatentsNoResultsMessage reports the SerpAPI / Google Patents JSON "no hits" case,
// which should trigger fallback queries rather than failing the whole source.
func isSerpPatentsNoResultsMessage(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	return strings.Contains(m, "hasn't returned any results") ||
		strings.Contains(m, "has not returned any results") ||
		(strings.Contains(m, "google patents") && strings.Contains(m, "no results"))
}

func fetchSerpAPIRelatedPatentIDs(ctx context.Context, apiKey, inventionText string, limit int) ([]string, error) {
	key := strings.Trim(strings.TrimSpace(apiKey), `"'`)
	if key == "" {
		return nil, fmt.Errorf("missing SerpAPI key")
	}

	queries := buildSerpPatentQueries(inventionText)
	if len(queries) == 0 {
		return nil, nil
	}

	num := limit * 3
	if num < 20 {
		num = 20
	}
	if num > 100 {
		num = 100
	}

	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	var lastErr error
	anySuccess := false

	for _, q := range queries {
		if len(out) >= limit {
			break
		}
		data, err := executeSerpPatentsSearch(ctx, key, q, num)
		if err != nil {
			lastErr = err
			continue
		}
		anySuccess = true
		if errMsg := strings.TrimSpace(data.Error); errMsg != "" {
			if isSerpPatentsNoResultsMessage(errMsg) {
				continue
			}
			return nil, fmt.Errorf("SerpAPI error: %s", errMsg)
		}
		for _, row := range data.OrganicResults {
			id, ok := patentIDFromSerpOrganicRow(row)
			if !ok {
				continue
			}
			k := normalizePatentIDKey(id)
			if k == "" {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, id)
			if len(out) >= limit {
				return out, nil
			}
		}
	}

	if !anySuccess && lastErr != nil {
		return nil, lastErr
	}

	return out, nil
}

func executeSerpPatentsSearch(ctx context.Context, apiKey, q string, num int) (*serpPatentsSearchResponse, error) {
	params := url.Values{}
	params.Set("engine", "google_patents")
	params.Set("q", q)
	params.Set("api_key", apiKey)
	params.Set("num", strconv.Itoa(num))
	params.Set("patents", "true")
	params.Set("scholar", "false")

	reqURL := serpAPIPatentsSearchURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SerpAPI Google Patents returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data serpPatentsSearchResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode SerpAPI response: %w", err)
	}

	return &data, nil
}

func patentIDFromSerpOrganicRow(row serpPatentsOrganicRow) (string, bool) {
	if row.IsScholar {
		return "", false
	}
	if strings.TrimSpace(row.ScholarID) != "" && strings.TrimSpace(row.PatentID) == "" {
		return "", false
	}

	pub := strings.TrimSpace(row.PublicationNumber)
	if pub != "" {
		return pub, true
	}

	pid := strings.TrimSpace(row.PatentID)
	if pid == "" {
		return "", false
	}
	if strings.HasPrefix(pid, "patent/") {
		rest := strings.TrimPrefix(pid, "patent/")
		parts := strings.Split(rest, "/")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0], true
		}
	}
	return pid, true
}
