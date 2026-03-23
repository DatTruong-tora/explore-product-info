package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

// Shared HTTP client to avoid connection bottlenecks.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// 1. Call the real UPC API.
func fetchSeedData(ctx context.Context, upc string) (*models.UPCResponse, error) {
	reqURL := fmt.Sprintf("https://api.upcitemdb.com/prod/trial/lookup?upc=%s", upc)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("UPC API returned HTTP %d", resp.StatusCode)
	}

	var data models.UPCResponse

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	fmt.Println("data", data)
	return &data, nil
}

// 2. Call the real USPTO API.
func fetchPatentData(ctx context.Context, brand string) (*models.PatentsViewResponse, error) {
	usptoKey := os.Getenv("USPTO_API_KEY")
	if usptoKey == "" {
		return nil, fmt.Errorf("missing USPTO_API_KEY environment variable")
	}

	searchText := fmt.Sprintf(`assignee:"%s"`, brand)
	reqURL := fmt.Sprintf("https://api.uspto.gov/api/v1/patent/applications/search?searchText=%s", url.QueryEscape(searchText))

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if (err != nil) {
		return nil, err
	}
	
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", strings.TrimSpace(usptoKey))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("USPTO API returned HTTP %d. Error: %s", resp.StatusCode, string(errBody))
	}

	var data models.PatentsViewResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode USPTO response: %v", err)
	}

	return &data, nil
}

// 3. Call the real LLM API (Gemini) for synthesis.
func synthesizeWithLLM(ctx context.Context, seedData *models.UPCResponse, patentData *models.PatentsViewResponse) (*models.FinalProductInfo, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	
	apiKey = strings.TrimSpace(apiKey)
	apiKey = strings.Trim(apiKey, `"'`) 

	if apiKey == "" {
		return nil, fmt.Errorf("missing GEMINI_API_KEY environment variable")
	}

	seedJSON, _ := json.Marshal(seedData)
	patentJSON, _ := json.Marshal(patentData)

	// Strict prompt that forces the LLM to extract Description and Features and map related patents.
	prompt := fmt.Sprintf(`You are an AI specializing in technical analysis and intellectual property. Analyze the Seed Data (Product) and Patent Data below.
	MANDATORY REQUIREMENT: Return ONLY one valid JSON string. Do not use markdown blocks (such as `+"```json"+`).

	Required JSON schema:
	{
		"product_id": "Generate a random ID",
		"product_identity": {"title": "", "brand": "", "model": "", "upc": ""},
		"product_description": "Extract the detailed description from Seed Data",
		"product_features": ["Feature 1", "Feature 2"],
		"core_invention_idea": "Summarize the core invention idea",
		"technical_specifications": {"key": "value"},
		"intellectual_property_analysis": {
			"associated_patents": ["Patent number 1"],
			"claim_charts": [
				{"patent_claim": "", "product_feature_mapped": "", "invalidity_search_relevance": "High/Medium/Low"}
			]
		}
	}

	Seed Data: %s
	Patent Data (Use up to the 2 most relevant patents): %s`, string(seedJSON), string(patentJSON))

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"response_mime_type": "application/json", // Force Gemini to return valid JSON.
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	// Use the active model endpoint.
	apiURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + apiKey
	
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("could not create Gemini request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Surface the detailed Google error response instead of guessing from the status code.
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body) // Read the detailed error body from Google.
		return nil, fmt.Errorf("Gemini API rejected the request with HTTP %d.\nGoogle error details: %s", resp.StatusCode, string(errBody))
	}

	var geminiResp models.GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("failed to decode Gemini response: %v", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("Gemini returned no results")
	}

	rawJSONString := geminiResp.Candidates[0].Content.Parts[0].Text
	rawJSONString = strings.TrimPrefix(rawJSONString, "```json\n")
	rawJSONString = strings.TrimSuffix(rawJSONString, "\n```")

	var finalData models.FinalProductInfo
	if err := json.Unmarshal([]byte(rawJSONString), &finalData); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from LLM: %v", err)
	}

	return &finalData, nil
}

// 4. Orchestrator: wrap the full flow.
func AggregateProductData(upcCode string) (*models.FinalProductInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) // Increase timeout for LLM processing.
	defer cancel()

	log.Printf("[1] Starting UPC data lookup for code %s...", upcCode)
	seedData, err := fetchSeedData(ctx, upcCode)
	if err != nil || len(seedData.Items) == 0 {
		return nil, fmt.Errorf("product information not found or UPC API call failed: %v", err)
	}
	brand := seedData.Items[0].Brand
	log.Printf("=> Found product: %s (Brand: %s)", seedData.Items[0].Title, brand)

	log.Printf("[2] Fetching patent data for %s...", brand)
	var wg sync.WaitGroup
	var patentData *models.PatentsViewResponse

	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := fetchPatentData(ctx, brand)
		if err != nil {
			log.Printf("=> Warning: failed to fetch patent data: %v", err)
			return
		}
		patentData = data
		log.Printf("=> Successfully fetched patent data for %s", brand)
	}()

	wg.Wait()

	log.Println("[3] Starting LLM synthesis...")
	finalResult, err := synthesizeWithLLM(ctx, seedData, patentData)
	if err != nil {
		return nil, fmt.Errorf("synthesis process failed: %v", err)
	}

	// Ensure the input UPC is present in the final response.
	finalResult.ProductIdentity.UPC = upcCode

	return finalResult, nil
}