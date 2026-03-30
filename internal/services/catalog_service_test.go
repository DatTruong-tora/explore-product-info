package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingDetailsProvider struct{}

func (failingDetailsProvider) EnrichProduct(context.Context, models.UPCItem) (*models.ProductEnrichment, error) {
	return nil, fmt.Errorf("provider unavailable")
}

func TestAggregateProductDataBackwardCompatibleShape(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	t.Setenv("USPTO_API_KEY", "test-uspto-key")
	t.Setenv("MILVUS_ADDRESS", "")
	t.Setenv("SERPAPI_API_KEY", "")

	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.Host, "upcitemdb.com") && strings.Contains(req.URL.Path, "/lookup"):
				return jsonResponse(http.StatusOK, `{
					"code":"OK",
					"items":[
						{
							"upc":"123456789012",
							"title":"Thermo Guard Teapot",
							"description":"Double-wall insulated teapot. Keeps drinks hot for hours. Ergonomic handle for safe pouring.",
							"brand":"Cha Misury Inc",
							"model":"GT-101",
							"category":"Kitchen Appliances",
							"images":["https://img.example.com/teapot-1.jpg"],
							"offers":[
								{"merchant":"Shop A","price":49.99,"currency":"USD","link":"https://shop.example.com/teapot"}
							]
						}
					]
				}`), nil
			case strings.Contains(req.URL.Host, "api.uspto.gov"):
				return jsonResponse(http.StatusOK, `{
					"count":1,
					"patentFileWrapperDataBag":[
						{
							"applicationNumberText":"US123",
							"applicationMetaData":{
								"filingDate":"2024-01-01",
								"firstInventorName":"Jane Doe",
								"applicantBag":[
									{
										"applicantNameText":"Cha Misury Inc",
										"correspondenceAddressBag":[{"countryName":"Korea"}]
									}
								]
							}
						}
					]
				}`), nil
			case strings.Contains(req.URL.Host, "generativelanguage.googleapis.com") && strings.Contains(req.URL.Path, ":generateContent"):
				return jsonResponse(http.StatusOK, `{
					"candidates":[
						{
							"content":{
								"parts":[
									{
										"text":"{\"polished_description\":\"Professional insulated teapot for everyday brewing and serving.\",\"core_invention_idea\":\"Improve heat retention with an insulated vessel and ergonomic pouring design.\",\"intellectual_property_analysis\":{\"associated_patents\":[\"US123\"],\"claim_charts\":[{\"patent_claim\":\"Insulated beverage vessel\",\"product_feature_mapped\":\"Double-wall insulated teapot\",\"invalidity_search_relevance\":\"High\"}]}}"
									}
								]
							}
						}
					]
				}`), nil
			default:
				return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
			}
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := AggregateProductData("123456789012")
	if err != nil {
		t.Fatalf("AggregateProductData returned error: %v", err)
	}

	if result.ProductIdentity.UPC != "123456789012" {
		t.Fatalf("expected UPC to be preserved, got %q", result.ProductIdentity.UPC)
	}
	if result.ProductIdentity.Title == "" || result.Description == "" || result.CoreInventionIdea == "" {
		t.Fatalf("expected legacy normalized fields to remain populated: %+v", result)
	}
	if result.CommercialDetails.Manufacturer != "Cha Misury Inc" {
		t.Fatalf("expected manufacturer mapping, got %q", result.CommercialDetails.Manufacturer)
	}
	if result.Stage != "marketed" {
		t.Fatalf("expected marketed stage fallback, got %q", result.Stage)
	}
	if len(result.Images) != 1 {
		t.Fatalf("expected images to be populated, got %v", result.Images)
	}
	if strings.TrimSpace(result.Features) == "" {
		t.Fatalf("expected features fallback to populate detailed features")
	}
}

func TestAggregateCompanyProductsDeduplicatesAndRespectsLimit(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("USPTO_API_KEY", "")
	t.Setenv("MILVUS_ADDRESS", "")
	t.Setenv("SERPAPI_API_KEY", "")

	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "upcitemdb.com") && strings.Contains(req.URL.Path, "/search") {
				return jsonResponse(http.StatusOK, `{
					"code":"OK",
					"total":3,
					"offset":0,
					"items":[
						{
							"upc":"111111111111",
							"title":"Acme Smart Kettle",
							"description":"Fast-boiling kettle with mobile app controls.",
							"brand":"Acme",
							"model":"AK-1",
							"category":"Kitchen Appliances",
							"offers":[{"merchant":"Shop A","price":59.99,"currency":"USD","link":"https://shop.example.com/a"}]
						},
						{
							"upc":"111111111111",
							"title":"Acme Smart Kettle Duplicate",
							"description":"Duplicate listing for the same kettle.",
							"brand":"Acme",
							"model":"AK-1",
							"category":"Kitchen Appliances"
						},
						{
							"upc":"222222222222",
							"title":"Acme Pour Over Kettle",
							"description":"Precision gooseneck kettle for manual brewing.",
							"brand":"Acme",
							"model":"AK-2",
							"category":"Kitchen Appliances"
						}
					]
				}`), nil
			}
			return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := AggregateCompanyProducts("Acme", 2, 0)
	if err != nil {
		t.Fatalf("AggregateCompanyProducts returned error: %v", err)
	}

	if result.Company != "Acme" {
		t.Fatalf("expected company to round-trip, got %q", result.Company)
	}
	if len(result.Products) != 2 {
		t.Fatalf("expected 2 deduplicated products, got %d", len(result.Products))
	}
	if result.Products[0].ProductIdentity.Brand != "Acme" || result.Products[1].ProductIdentity.Brand != "Acme" {
		t.Fatalf("expected company-filtered products, got %+v", result.Products)
	}
	if result.Products[0].ProductIdentity.UPC == result.Products[1].ProductIdentity.UPC {
		t.Fatalf("expected duplicate UPCs to be collapsed")
	}
}

func TestAggregateProductFromItemIgnoresProviderFailureInLenientMode(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("USPTO_API_KEY", "")
	t.Setenv("MILVUS_ADDRESS", "")
	t.Setenv("SERPAPI_API_KEY", "")

	item := models.UPCItem{
		UPC:         "999999999999",
		Title:       "Fallback Product",
		Description: "Designed for daily use. Includes an insulated body. Built for convenient handling.",
		Brand:       "Acme",
		Model:       "FP-1",
		Offers: []models.Offer{
			{Merchant: "Shop A", Price: 29.99, Currency: "USD", Link: "https://shop.example.com/fallback"},
		},
	}

	result, err := aggregateProductFromItem(context.Background(), item, []productDetailsProvider{failingDetailsProvider{}}, aggregateOptions{
		strictSynthesis:  false,
		persistEmbedding: false,
	})
	if err != nil {
		t.Fatalf("aggregateProductFromItem returned error: %v", err)
	}

	if result.Stage != "marketed" {
		t.Fatalf("expected lenient stage fallback, got %q", result.Stage)
	}
	if strings.TrimSpace(result.Features) == "" {
		t.Fatalf("expected description-based feature fallback")
	}
}

func TestBuildDetailedFeaturesDoesNotAppendDoublePeriod(t *testing.T) {
	result := buildDetailedFeatures(&models.FinalProductInfo{
		Description:       "A large multi-touch tablet for work and creativity.",
		CoreInventionIdea: "Provide an intuitive portable computing experience.",
	}, models.UPCItem{})

	if strings.Contains(result, "..") {
		t.Fatalf("expected features to avoid double periods, got %q", result)
	}
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
