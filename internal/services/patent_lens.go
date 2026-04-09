package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// lensPatentSearchURL is the Lens.org patent search endpoint (tests may override).
var lensPatentSearchURL = "https://api.lens.org/patent/search"

type lensResponse struct {
	Data []struct {
		DocNumber string `json:"doc_number"`
	} `json:"data"`
}

type lensSearchRequest struct {
	Query   lensQuery `json:"query"`
	Include []string  `json:"include"`
	Size    int       `json:"size"`
	From    int       `json:"from"`
}

type lensQuery struct {
	QueryString lensQueryString `json:"query_string"`
}

type lensQueryString struct {
	Query string `json:"query"`
}

// fetchLensOrgRelatedPatentIDs searches Lens.org using keyPhrases joined as the query string.
// If keyPhrases is empty, falls back to cleaned inventionText (compact prose).
func fetchLensOrgRelatedPatentIDs(ctx context.Context, apiKey, inventionText string, keyPhrases []string, limit int) ([]string, error) {
	key := strings.Trim(strings.TrimSpace(apiKey), `"'`)
	if key == "" {
		return nil, fmt.Errorf("missing Lens API key")
	}

	var parts []string
	for _, kp := range keyPhrases {
		kp = strings.TrimSpace(kp)
		if kp != "" {
			parts = append(parts, kp)
		}
	}
	queryStr := strings.Join(parts, " ")
	if strings.TrimSpace(queryStr) == "" {
		queryStr = strings.TrimSpace(inventionText)
	}
	if queryStr == "" {
		return nil, nil
	}

	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)
	fetched := 0
	for pageIdx := 0; len(out) < limit; pageIdx++ {
		currentSize := limit - len(out)
		if currentSize > 100 {
			currentSize = 100
		}

		body := lensSearchRequest{
			Query: lensQuery{
				QueryString: lensQueryString{Query: queryStr},
			},
			Include: []string{"doc_number"},
			Size:    currentSize,
			From:    fetched,
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode Lens request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, lensPatentSearchURL, bytes.NewReader(payload))
		if err != nil {
			if pageIdx == 0 {
				return nil, err
			}
			log.Printf("Lens.org patent search pagination warning: %v", err)
			return out, nil
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			if pageIdx == 0 {
				return nil, err
			}
			log.Printf("Lens.org patent search pagination warning: %v", err)
			return out, nil
		}

		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if pageIdx == 0 {
				return nil, err
			}
			log.Printf("Lens.org patent search pagination warning: %v", err)
			return out, nil
		}

		if resp.StatusCode != http.StatusOK {
			if pageIdx == 0 {
				return nil, fmt.Errorf("Lens.org patent search returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
			}
			log.Printf("Lens.org patent search pagination warning: HTTP %d", resp.StatusCode)
			return out, nil
		}

		var parsed lensResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			if pageIdx == 0 {
				return nil, fmt.Errorf("decode Lens response: %w", err)
			}
			log.Printf("Lens.org patent search pagination warning: decode response: %v", err)
			return out, nil
		}

		if len(parsed.Data) == 0 {
			break
		}

		for _, row := range parsed.Data {
			if len(out) >= limit {
				break
			}
			id := strings.TrimSpace(row.DocNumber)
			if id == "" {
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
		}

		n := len(parsed.Data)
		fetched += n
		if n < currentSize {
			break
		}
	}

	return out, nil
}
