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

	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

// Shared HTTP client to avoid connection bottlenecks.
var httpClient = &http.Client{Timeout: 60 * time.Second}

const (
	milvusCollectionName = "product_info"
	vectorFieldName      = "vector"
	titleFieldName       = "title"
	vectorDimension      = 768
)

func newMilvusClient(ctx context.Context) (client.Client, error) {
	address := strings.TrimSpace(os.Getenv("MILVUS_ADDRESS"))
	if address == "" {
		return nil, fmt.Errorf("missing MILVUS_ADDRESS environment variable")
	}

	c, err := client.NewClient(ctx, client.Config{
		Address: address,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Milvus: %v", err)
	}

	return c, nil
}

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

// ---------------- NEW FUNCTIONS FOR MILVUS AND EMBEDDING ----------------
// Transform text to vector embedding using Gemini Embedding API
func generateEmbedding(ctx context.Context, text string) ([]float32, error) {
	apiKey := strings.Trim(strings.TrimSpace(os.Getenv("GEMINI_API_KEY")), `"'`)
	if apiKey == "" {
		return nil, fmt.Errorf("missing GEMINI_API_KEY environment variable")
	}

	apiURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent"

	reqBody := map[string]interface{}{
		"model": "models/gemini-embedding-001",
		"content": map[string]interface{}{
			"parts": []map[string]string{{"text": text}},
		},
		"output_dimensionality": 768,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("could not create Embedding request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Embedding API failed with status %d. Google error details: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding.Values, nil
}

// Connect and save data to Milvus
func saveToMilvus(ctx context.Context, finalInfo *models.FinalProductInfo) error {
	c, err := newMilvusClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	has, err := c.HasCollection(ctx, milvusCollectionName)
	if err != nil {
		return err
	}

	if !has {
		// Define the Schema (Structure of the table in Milvus)
		schema := entity.NewSchema().WithName(milvusCollectionName).WithDescription("Store product embeddings for semantic search")
		// Column 1: Product ID (Primary Key)
		schema.WithField(entity.NewField().WithName("product_id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(256).WithIsPrimaryKey(true))
		// Column 2: Title (To display quickly)
		schema.WithField(entity.NewField().WithName(titleFieldName).WithDataType(entity.FieldTypeVarChar).WithMaxLength(1000))
		// Column 3: Vector embedding (requested as 768 dimensions from Gemini)
		schema.WithField(entity.NewField().WithName(vectorFieldName).WithDataType(entity.FieldTypeFloatVector).WithDim(vectorDimension))

		err = c.CreateCollection(ctx, schema, entity.DefaultShardNumber)
		if err != nil {
			return fmt.Errorf("failed to create Milvus collection: %v", err)
		}

		// Must create Index for the vector column to allow Milvus to search
		idx, _ := entity.NewIndexHNSW(entity.COSINE, 8, 200)
		err = c.CreateIndex(ctx, milvusCollectionName, vectorFieldName, idx, false)
		if err != nil {
			return fmt.Errorf("failed to create index: %v", err)
		}
	}

	// Prepare data to Insert
	idCol := entity.NewColumnVarChar("product_id", []string{finalInfo.ProductID})
	titleCol := entity.NewColumnVarChar(titleFieldName, []string{finalInfo.ProductIdentity.Title})
	vectorCol := entity.NewColumnFloatVector(vectorFieldName, vectorDimension, [][]float32{finalInfo.Vector})

	// Insert into Milvus
	_, err = c.Insert(ctx, milvusCollectionName, "", idCol, titleCol, vectorCol)
	if err != nil {
		return fmt.Errorf("failed to insert vector to Milvus: %v", err)
	}

	// Push data from RAM to the Hard Disk of Milvus
	c.Flush(ctx, milvusCollectionName, false)
	return nil
}

func SearchProducts(query string, limit int, minScore float32) (*models.SearchResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := newMilvusClient(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	response := &models.SearchResponse{
		Query:    query,
		Limit:    limit,
		MinScore: minScore,
		Matches:  make([]models.SearchMatch, 0),
	}

	has, err := c.HasCollection(ctx, milvusCollectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to check Milvus collection: %v", err)
	}
	if !has {
		return response, nil
	}

	if err := c.LoadCollection(ctx, milvusCollectionName, false); err != nil {
		return nil, fmt.Errorf("failed to load Milvus collection: %v", err)
	}

	queryVector, err := generateEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate search embedding: %v", err)
	}

	searchParam, err := entity.NewIndexHNSWSearchParam(64)
	if err != nil {
		return nil, fmt.Errorf("failed to create Milvus search params: %v", err)
	}

	results, err := c.Search(
		ctx,
		milvusCollectionName,
		[]string{},
		"",
		[]string{titleFieldName},
		[]entity.Vector{entity.FloatVector(queryVector)},
		vectorFieldName,
		entity.COSINE,
		limit,
		searchParam,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search Milvus: %v", err)
	}

	if len(results) == 0 {
		return response, nil
	}

	result := results[0]
	idCol, ok := result.IDs.(*entity.ColumnVarChar)
	if !ok {
		return nil, fmt.Errorf("unexpected Milvus ID column type")
	}

	titleColumn := result.Fields.GetColumn(titleFieldName)
	titleCol, ok := titleColumn.(*entity.ColumnVarChar)
	if !ok {
		return nil, fmt.Errorf("unexpected Milvus title column type")
	}

	for i := 0; i < result.ResultCount; i++ {
		score := result.Scores[i]
		if score < minScore {
			continue
		}

		productID, err := idCol.ValueByIdx(i)
		if err != nil {
			return nil, fmt.Errorf("failed to read Milvus product ID: %v", err)
		}

		title, err := titleCol.ValueByIdx(i)
		if err != nil {
			return nil, fmt.Errorf("failed to read Milvus title: %v", err)
		}

		response.Matches = append(response.Matches, models.SearchMatch{
			ProductID: productID,
			Title:     title,
			Score:     score,
		})
	}

	return response, nil
}

func persistEmbeddingAsync(productID, title, textToEmbed string) {
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		log.Println("[4] Generating vector embedding in background...")
		vector, err := generateEmbedding(bgCtx, textToEmbed)
		if err != nil {
			log.Printf("Warning: Failed to generate embedding: %v", err)
			return
		}

		record := &models.FinalProductInfo{
			ProductID: productID,
			ProductIdentity: models.ProductIdentity{
				Title: title,
			},
			Vector: vector,
		}

		log.Println("[5] Saving data to Milvus in background...")
		if err := saveToMilvus(bgCtx, record); err != nil {
			log.Printf("Warning: Failed to save to Milvus: %v", err)
			return
		}

		log.Printf("=> Successfully saved Product %s to Milvus Database!", productID)
	}()
}

// 4. Orchestrator: Manual mapping here
func AggregateProductData(upcCode string) (*models.FinalProductInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// 4.1. Fetch the product data from UPC (Contains links, prices, descriptions)
	log.Println("[1] Fetching product data from UPC...")
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
	log.Println("[2] Fetching patent data from USPTO...")
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
		foundManufacturer, foundCountry, foundFilingDate, foundInventor := false, false, false, false

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

	// 4.7. Offload embedding generation and Milvus persistence off the critical path.
	textToEmbed := fmt.Sprintf("Title: %s. Brand: %s. Core Idea: %s. Description: %s",
		finalResult.ProductIdentity.Title,
		finalResult.ProductIdentity.Brand,
		finalResult.CoreInventionIdea,
		finalResult.Description)

	persistEmbeddingAsync(finalResult.ProductID, finalResult.ProductIdentity.Title, textToEmbed)

	return finalResult, nil
}
