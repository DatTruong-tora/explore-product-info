package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

type productDiscoveryProvider interface {
	LookupByUPC(ctx context.Context, upc string) (*models.UPCItemDBResponse, error)
	SearchByCompany(ctx context.Context, company string, limit, offset int) (*models.UPCItemDBResponse, error)
}

type productDetailsProvider interface {
	EnrichProduct(ctx context.Context, item models.UPCItem) (*models.ProductEnrichment, error)
}

var errUPCSearchNotFound = errors.New("upc search not found")

type upcItemDBProvider struct{}

func (upcItemDBProvider) LookupByUPC(ctx context.Context, upc string) (*models.UPCItemDBResponse, error) {
	return fetchSeedData(ctx, upc)
}

func (upcItemDBProvider) SearchByCompany(ctx context.Context, company string, limit, offset int) (*models.UPCItemDBResponse, error) {
	query := strings.TrimSpace(company)
	if query == "" {
		return nil, fmt.Errorf("company is required")
	}

	searchAttempts := []url.Values{
		buildCompanySearchParams(query, offset, true),
		buildCompanySearchParams(query, offset, false),
	}

	for _, params := range searchAttempts {
		data, err := searchUPCItemDB(ctx, params)
		if err == nil {
			return data, nil
		}
		if errors.Is(err, errUPCSearchNotFound) {
			continue
		}
		return nil, err
	}

	return &models.UPCItemDBResponse{
		Code:   "NOT_FOUND",
		Offset: offset,
		Items:  []models.UPCItem{},
	}, nil
}

func buildCompanySearchParams(company string, offset int, includeBrand bool) url.Values {
	params := url.Values{}
	params.Set("s", company)
	if includeBrand {
		params.Set("brand", company)
	}
	if offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", offset))
	}
	return params
}

func searchUPCItemDB(ctx context.Context, params url.Values) (*models.UPCItemDBResponse, error) {
	reqURL := "https://api.upcitemdb.com/prod/trial/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errUPCSearchNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("UPC search API returned HTTP %d", resp.StatusCode)
	}

	var data models.UPCItemDBResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return &data, nil
}

type serpAPIProvider struct {
	apiKey string
}

func defaultProductDiscoveryProvider() productDiscoveryProvider {
	return upcItemDBProvider{}
}

// SerpAPIKey returns the SerpAPI credential, preferring SERP_API_KEY over SERPAPI_API_KEY.
func SerpAPIKey() string {
	if k := strings.Trim(strings.TrimSpace(os.Getenv("SERP_API_KEY")), `"'`); k != "" {
		return k
	}
	return strings.Trim(strings.TrimSpace(os.Getenv("SERPAPI_API_KEY")), `"'`)
}

func defaultProductDetailsProviders() []productDetailsProvider {
	apiKey := SerpAPIKey()
	if apiKey == "" {
		return nil
	}

	return []productDetailsProvider{
		serpAPIProvider{apiKey: apiKey},
	}
}

func (p serpAPIProvider) EnrichProduct(ctx context.Context, item models.UPCItem) (*models.ProductEnrichment, error) {
	if p.apiKey == "" {
		return nil, nil
	}

	queryParts := []string{item.Brand, item.Title, item.Model}
	query := strings.TrimSpace(strings.Join(queryParts, " "))
	if query == "" {
		return nil, nil
	}

	params := url.Values{}
	params.Set("engine", "google_shopping")
	params.Set("q", query)
	params.Set("gl", "us")
	params.Set("hl", "en")
	params.Set("api_key", p.apiKey)

	reqURL := "https://serpapi.com/search.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SerpAPI returned HTTP %d", resp.StatusCode)
	}

	var data models.SerpAPIShoppingResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	enrichment := &models.ProductEnrichment{
		Images: make([]string, 0),
	}

	featureParts := make([]string, 0)

	expected := normalizeSearchText(strings.Join([]string{item.Brand, item.Title, item.Model}, " "))
	for _, result := range data.ShoppingResults {
		candidate := normalizeSearchText(strings.Join([]string{result.Title, result.Snippet, result.Source}, " "))
		if expected != "" && candidate != "" && !strings.Contains(candidate, expected) && !strings.Contains(expected, candidate) {
			continue
		}

		if result.Thumbnail != "" {
			enrichment.Images = append(enrichment.Images, result.Thumbnail)
		}

		for _, extension := range result.Extensions {
			if strings.TrimSpace(extension) != "" {
				featureParts = append(featureParts, strings.TrimSpace(extension))
			}
		}

		if enrichment.Description == "" && strings.TrimSpace(result.Snippet) != "" {
			enrichment.Description = strings.TrimSpace(result.Snippet)
		}
	}

	enrichment.Images = uniqueStrings(enrichment.Images)
	enrichment.Features = joinFeatureParts(featureParts)

	if len(enrichment.Images) == 0 && enrichment.Features == "" && enrichment.Description == "" {
		return nil, nil
	}

	return enrichment, nil
}
