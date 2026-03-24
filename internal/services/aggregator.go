package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

// Shared HTTP client to avoid connection bottlenecks.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// 1. Call the UPC API.
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

	return &data, nil
}

// 2. Call the USPTO API.
func fetchPatentData(ctx context.Context, brand string) (*models.PatentsViewResponse, error) {
	usptoKey := os.Getenv("USPTO_API_KEY")
	if usptoKey == "" {
		return nil, fmt.Errorf("missing USPTO_API_KEY environment variable")
	}

	searchText := fmt.Sprintf(`assignee:"%s"`, brand)
	reqURL := fmt.Sprintf("https://api.uspto.gov/api/v1/patent/applications/search?searchText=%s", url.QueryEscape(searchText))

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
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

// 3. Call the  LLM API (Gemini) for synthesis.
func generateRandomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "PRD-" + hex.EncodeToString(b)
}

func synthesizeWithLLM(ctx context.Context, description string, patentData *models.PatentsViewResponse) (*models.LLMAnalysisResult, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")

	apiKey = strings.TrimSpace(apiKey)
	apiKey = strings.Trim(apiKey, `"'`)

	if apiKey == "" {
		return nil, fmt.Errorf("missing GEMINI_API_KEY environment variable")
	}

	// Only send what the LLM really needs to analyze
	patentJSON, _ := json.Marshal(patentData)

	prompt := fmt.Sprintf(`You are an AI specializing in intellectual property and product analysis. 
	Analyze the Product Description, Features, and Patent Data below.
	Notice: The Product Description might be truncated or cut off at the end. 

	MANDATORY REQUIREMENT: Return ONLY a valid JSON string matching the exact schema. No markdown blocks.

	Required JSON schema:
	{
		"polished_description": "Read the raw Product Description. If it is cut off, complete the final sentence gracefully based on context. Rewrite it to be a professional, fully complete paragraph.",
		"core_invention_idea": "Summarize the core invention idea based on the features and patents",
		"intellectual_property_analysis": {
			"associated_patents": ["Patent applicationNumberText"],
			"claim_charts": [
				{"patent_claim": "Claim or concept from patent", "product_feature_mapped": "Feature mapped", "invalidity_search_relevance": "High/Medium/Low"}
			]
		}
	}

	Product Description: %s
	Patent Data: %s`, description, string(patentJSON))

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"response_mime_type": "application/json",
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

	rawJSONString := strings.TrimSuffix(strings.TrimPrefix(geminiResp.Candidates[0].Content.Parts[0].Text, "```json\n"), "\n```")

	var analysis models.LLMAnalysisResult
	if err := json.Unmarshal([]byte(rawJSONString), &analysis); err != nil {
		return nil, err
	}

	return &analysis, nil
}

// 4. Orchestrator: Manual mapping here
func AggregateProductData(upcCode string) (*models.FinalProductInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// 4.1. Fetch the product data from UPC (Contains links, prices, descriptions)
	seedData, err := fetchSeedData(ctx, upcCode)
	if err != nil || len(seedData.Items) == 0 {
		return nil, fmt.Errorf("UPC lookup failed: %v", err)
	}
	item := seedData.Items[0]

	// 4.2. Initialize the return object and manually map the Seed data
	finalResult := &models.FinalProductInfo{
		ProductID: generateRandomID(),
		ProductIdentity: models.ProductIdentity{
			Title: item.Title,
			Brand: item.Brand,
			Model: item.Model,
			UPC:   upcCode,
		},
		Description: item.Description,
		TechSpecs:   make(map[string]string),
		CommercialDetails: models.CommercialInfo{
			Manufacturer: item.Brand,
			Marketplaces: make([]models.Marketplace, 0),
		},
	}

	// --> Map commercial data (Links, Average Price) from the Offers array <---
	var totalPrice float64
	var offerCount int
	for _, offer := range item.Offers {
		finalResult.CommercialDetails.Marketplaces = append(finalResult.CommercialDetails.Marketplaces, models.Marketplace{
			StoreName:   offer.Merchant,
			Price:       offer.Price,
			Currency:    offer.Currency,
			ProductLink: offer.Link,
		})
		totalPrice += offer.Price
		offerCount++
	}

	// Calculate the average price
	if offerCount > 0 {
		finalResult.CommercialDetails.AveragePrice = totalPrice / float64(offerCount)
	}

	// 4.3. Fetch patent data
	var wg sync.WaitGroup
	var patentData *models.PatentsViewResponse

	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := fetchPatentData(ctx, item.Brand)
		if err == nil {
			patentData = data
		} else {
			log.Printf("Warning: patent fetch failed: %v", err)
		}
	}()
	wg.Wait()

	// 4.4. Manual mapping of patent data to technical specs
	if patentData != nil && len(patentData.PatentFileWrapperDataBag) > 0 {
		foundManufacturer := false
		foundCountry := false
		foundFilingDate := false
		foundInventor := false

		for _, patentItem := range patentData.PatentFileWrapperDataBag {
			meta := patentItem.ApplicationMetaData

			if !foundFilingDate && meta.FilingDate != "" {
				finalResult.TechSpecs["patent_filing_date"] = meta.FilingDate
				foundFilingDate = true
			}

			if !foundInventor && meta.FirstInventorName != "" {
				finalResult.TechSpecs["key_inventor"] = meta.FirstInventorName
				foundInventor = true
			}

			for _, applicant := range meta.ApplicantBag {
				if !foundManufacturer && applicant.ApplicantNameText != "" {
					finalResult.CommercialDetails.Manufacturer = applicant.ApplicantNameText
					foundManufacturer = true
				}

				for _, address := range applicant.CorrespondenceAddressBag {
					if !foundCountry && address.CountryName != "" {
						finalResult.CommercialDetails.CountryOfOrigin = address.CountryName
						foundCountry = true
					}
				}
			}

			if foundFilingDate && foundInventor && foundManufacturer && foundCountry {
				break
			}
		}
	}

	// 4.5. Call LLM to do the complex part (Only Core Idea and Claim Charts)
	log.Println("[3] Starting LLM synthesis for IP Analysis...")
	llmAnalysis, err := synthesizeWithLLM(ctx, item.Description, patentData)
	if err != nil {
		return nil, fmt.Errorf("synthesis process failed: %v", err)
	}

	// 4.6. Merge the result from LLM into the Struct
	finalResult.Description = llmAnalysis.PolishedDescription
	finalResult.CoreInventionIdea = llmAnalysis.CoreInventionIdea
	finalResult.IPAnalysis = llmAnalysis.IPAnalysis

	return finalResult, nil
}
