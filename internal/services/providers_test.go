package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSearchByCompanyUsesBrandAwareSearch(t *testing.T) {
	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("brand") != "Apple" {
				t.Fatalf("expected brand filter to be set, got %q", req.URL.Query().Get("brand"))
			}
			if req.URL.Query().Get("s") != "Apple" {
				t.Fatalf("expected search term to be Apple, got %q", req.URL.Query().Get("s"))
			}

			return jsonResponse(http.StatusOK, `{
				"code":"OK",
				"total":1,
				"offset":0,
				"items":[{"upc":"123","title":"Apple Product","brand":"Apple"}]
			}`), nil
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := upcItemDBProvider{}.SearchByCompany(context.Background(), "Apple", 3, 0)
	if err != nil {
		t.Fatalf("SearchByCompany returned error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
}

func TestSearchByCompanyFallsBackAfterBrandSearchNotFound(t *testing.T) {
	callCount := 0
	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				if req.URL.Query().Get("brand") != "Acme" {
					t.Fatalf("expected first attempt to include brand filter, got %q", req.URL.Query().Get("brand"))
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}

			if req.URL.Query().Get("brand") != "" {
				t.Fatalf("expected fallback attempt without brand filter, got %q", req.URL.Query().Get("brand"))
			}

			return jsonResponse(http.StatusOK, `{
				"code":"OK",
				"total":1,
				"offset":0,
				"items":[{"upc":"456","title":"Acme Product","brand":"Acme"}]
			}`), nil
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := upcItemDBProvider{}.SearchByCompany(context.Background(), "Acme", 3, 0)
	if err != nil {
		t.Fatalf("SearchByCompany returned error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item from fallback, got %d", len(result.Items))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 search attempts, got %d", callCount)
	}
}

func TestSearchByCompanyReturnsEmptyResponseWhenNoMatch(t *testing.T) {
	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	result, err := upcItemDBProvider{}.SearchByCompany(context.Background(), "UnknownCo", 3, 0)
	if err != nil {
		t.Fatalf("expected no hard error on not found, got %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected empty result set, got %d items", len(result.Items))
	}
	if result.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND code, got %q", result.Code)
	}
}

func TestSearchUPCItemDBPropagatesNon404Errors(t *testing.T) {
	originalClient := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network down")
		}),
	}
	t.Cleanup(func() {
		httpClient = originalClient
	})

	_, err := upcItemDBProvider{}.SearchByCompany(context.Background(), "Apple", 3, 0)
	if err == nil {
		t.Fatalf("expected network error to propagate")
	}
}
