package services

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestBuildPatentSearchQueries(t *testing.T) {
	queries := buildPatentSearchQueries("portable bio-signal measuring device")
	if len(queries) != 3 {
		t.Fatalf("expected 3 query variants, got %d", len(queries))
	}
	if queries[0] != `inventionTitle:"portable bio-signal measuring device"` {
		t.Fatalf("unexpected exact invention-title query: %q", queries[0])
	}
	if queries[1] != `"portable bio-signal measuring device"` {
		t.Fatalf("unexpected quoted phrase query: %q", queries[1])
	}
	if !strings.Contains(queries[2], "portable") || !strings.Contains(queries[2], "measuring") {
		t.Fatalf("unexpected keyword query: %q", queries[2])
	}
}

func TestFindRelatedPatentIDsAggregatesAcrossQueryVariants(t *testing.T) {
	t.Setenv("USPTO_API_KEY", "test-uspto-key")

	originalClient := httpClient
	callCount := 0
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			searchText := req.URL.Query().Get("searchText")

			switch searchText {
			case `inventionTitle:"portable bio-signal measuring device"`:
				return jsonResponse(http.StatusOK, `{
					"count":2,
					"patentFileWrapperDataBag":[
						{"applicationNumberText":"US100"},
						{"applicationNumberText":"US101"}
					]
				}`), nil
			case `"portable bio-signal measuring device"`:
				return jsonResponse(http.StatusOK, `{
					"count":2,
					"patentFileWrapperDataBag":[
						{"applicationNumberText":"US101"},
						{"applicationNumberText":"US102"}
					]
				}`), nil
			default:
				return jsonResponse(http.StatusOK, `{
					"count":2,
					"patentFileWrapperDataBag":[
						{"applicationNumberText":"US103"},
						{"applicationNumberText":"US104"}
					]
				}`), nil
			}
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := FindRelatedPatentIDs("portable bio-signal measuring device", 4)
	if err != nil {
		t.Fatalf("FindRelatedPatentIDs returned error: %v", err)
	}

	if len(result.PatentIDs) != 4 {
		t.Fatalf("expected 4 deduplicated patent IDs, got %d", len(result.PatentIDs))
	}
	if result.PatentIDs[0] != "US100" || result.PatentIDs[1] != "US101" || result.PatentIDs[2] != "US102" || result.PatentIDs[3] != "US103" {
		t.Fatalf("unexpected patent ID ordering: %#v", result.PatentIDs)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 USPTO calls, got %d", callCount)
	}
}

func TestFindRelatedPatentIDsHonorsLimit(t *testing.T) {
	t.Setenv("USPTO_API_KEY", "test-uspto-key")

	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
				"count":3,
				"patentFileWrapperDataBag":[
					{"applicationNumberText":"US200"},
					{"applicationNumberText":"US201"},
					{"applicationNumberText":"US202"}
				]
			}`), nil
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := FindRelatedPatentIDs("portable sensor", 2)
	if err != nil {
		t.Fatalf("FindRelatedPatentIDs returned error: %v", err)
	}
	if len(result.PatentIDs) != 2 {
		t.Fatalf("expected limit of 2 patent IDs, got %d", len(result.PatentIDs))
	}
}

func TestFetchPatentDataBySearchTextRequiresAPIKey(t *testing.T) {
	t.Setenv("USPTO_API_KEY", "")

	_, err := fetchPatentDataBySearchText(context.Background(), `inventionTitle:"portable sensor"`)
	if err == nil {
		t.Fatalf("expected missing USPTO key error")
	}
}

func TestFetchPatentDataBySearchTextPropagatesHTTPErrors(t *testing.T) {
	t.Setenv("USPTO_API_KEY", "test-uspto-key")

	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network error")
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	_, err := fetchPatentDataBySearchText(context.Background(), `inventionTitle:"portable sensor"`)
	if err == nil {
		t.Fatalf("expected network error")
	}
}
