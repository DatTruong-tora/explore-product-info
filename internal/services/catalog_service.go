package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

type aggregateOptions struct {
	strictSynthesis  bool
	persistEmbedding bool
}

func AggregateProductData(upcCode string) (*models.FinalProductInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	log.Println("[1] Fetching product data from UPC...")
	discovery := defaultProductDiscoveryProvider()
	seedData, err := discovery.LookupByUPC(ctx, upcCode)
	if err != nil || len(seedData.Items) == 0 {
		return nil, fmt.Errorf("UPC lookup failed: %v", err)
	}

	item := seedData.Items[0]
	if strings.TrimSpace(item.UPC) == "" {
		item.UPC = upcCode
	}

	return aggregateProductFromItem(ctx, item, defaultProductDetailsProviders(), aggregateOptions{
		strictSynthesis:  true,
		persistEmbedding: true,
	})
}

func AggregateCompanyProducts(company string, limit, offset int) (*models.CompanyProductsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	discovery := defaultProductDiscoveryProvider()
	searchResult, err := discovery.SearchByCompany(ctx, company, limit, offset)
	if err != nil {
		return nil, err
	}

	response := &models.CompanyProductsResponse{
		Company:  company,
		Total:    searchResult.Total,
		Offset:   searchResult.Offset,
		Limit:    limit,
		Products: make([]models.FinalProductInfo, 0, len(searchResult.Items)),
	}

	detailProviders := defaultProductDetailsProviders()
	seen := make(map[string]struct{})
	for _, item := range searchResult.Items {
		if !matchesCompany(item, company) {
			continue
		}

		key := productDedupKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		productInfo, err := aggregateProductFromItem(ctx, item, detailProviders, aggregateOptions{
			strictSynthesis:  false,
			persistEmbedding: true,
		})
		if err != nil {
			log.Printf("Warning: failed to enrich company product %q: %v", item.Title, err)
			continue
		}

		response.Products = append(response.Products, *productInfo)
		if limit > 0 && len(response.Products) >= limit {
			break
		}
	}

	return response, nil
}

func aggregateProductFromItem(ctx context.Context, item models.UPCItem, detailProviders []productDetailsProvider, options aggregateOptions) (*models.FinalProductInfo, error) {
	finalResult := newBaseProductInfo(item)

	var patentData *models.PatentsViewResponse
	if strings.TrimSpace(item.Brand) != "" {
		log.Println("[2] Fetching patent data from USPTO...")
		data, err := fetchPatentData(ctx, item.Brand)
		if err == nil {
			patentData = data
		} else {
			log.Printf("Warning: patent fetch failed: %v", err)
		}
	}

	mapPatentData(finalResult, patentData)

	if enrichment, err := enrichProduct(ctx, item, detailProviders); err != nil {
		log.Printf("Warning: provider enrichment failed for %q: %v", item.Title, err)
	} else {
		mergeProductEnrichment(finalResult, enrichment)
	}

	log.Println("[3] Starting LLM synthesis for IP Analysis...")
	if description := strings.TrimSpace(finalResult.Description); description != "" {
		llmAnalysis, err := synthesizeWithLLM(ctx, description, patentData)
		if err != nil {
			if options.strictSynthesis {
				return nil, fmt.Errorf("synthesis process failed: %v", err)
			}
			log.Printf("Warning: synthesis skipped for %q: %v", item.Title, err)
		} else {
			finalResult.Description = llmAnalysis.PolishedDescription
			finalResult.CoreInventionIdea = llmAnalysis.CoreInventionIdea
			finalResult.IPAnalysis = llmAnalysis.IPAnalysis
		}
	}

	applyFallbackFields(finalResult, item, patentData)

	// if options.persistEmbedding {
	// 	textToEmbed := fmt.Sprintf("Title: %s. Brand: %s. Core Idea: %s. Description: %s",
	// 		finalResult.ProductIdentity.Title,
	// 		finalResult.ProductIdentity.Brand,
	// 		finalResult.CoreInventionIdea,
	// 		finalResult.Description)
	// 	persistEmbeddingAsync(finalResult.ProductID, finalResult.ProductIdentity.Title, textToEmbed)
	// }

	return finalResult, nil
}

func newBaseProductInfo(item models.UPCItem) *models.FinalProductInfo {
	upc := strings.TrimSpace(item.UPC)
	if upc == "" {
		upc = strings.TrimLeft(strings.TrimSpace(item.EAN), "0")
	}

	result := &models.FinalProductInfo{
		ProductID: generateRandomID(),
		ProductIdentity: models.ProductIdentity{
			Title:    item.Title,
			Brand:    item.Brand,
			Model:    item.Model,
			UPC:      upc,
			Category: item.Category,
		},
		CommercialDetails: models.CommercialInfo{
			Manufacturer: item.Brand,
			Marketplaces: make([]models.Marketplace, 0, len(item.Offers)),
		},
		Description: strings.TrimSpace(item.Description),
		Images:      uniqueStrings(item.Images),
		TechSpecs:   make(map[string]string),
	}

	var totalPrice float64
	var offerCount int
	for _, offer := range item.Offers {
		result.CommercialDetails.Marketplaces = append(result.CommercialDetails.Marketplaces, models.Marketplace{
			StoreName:   offer.Merchant,
			Price:       offer.Price,
			Currency:    offer.Currency,
			ProductLink: offer.Link,
		})
		if offer.Price > 0 {
			totalPrice += offer.Price
			offerCount++
		}
	}
	if offerCount > 0 {
		result.CommercialDetails.AveragePrice = totalPrice / float64(offerCount)
	}

	if strings.TrimSpace(item.Color) != "" {
		result.TechSpecs["color"] = strings.TrimSpace(item.Color)
	}
	if strings.TrimSpace(item.Size) != "" {
		result.TechSpecs["size"] = strings.TrimSpace(item.Size)
	}
	if strings.TrimSpace(item.Dimension) != "" {
		result.TechSpecs["dimension"] = strings.TrimSpace(item.Dimension)
	}
	if strings.TrimSpace(item.Weight) != "" {
		result.TechSpecs["weight"] = strings.TrimSpace(item.Weight)
	}

	return result
}

func mapPatentData(finalResult *models.FinalProductInfo, patentData *models.PatentsViewResponse) {
	if patentData == nil || len(patentData.PatentFileWrapperDataBag) == 0 {
		return
	}

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

func enrichProduct(ctx context.Context, item models.UPCItem, providers []productDetailsProvider) (*models.ProductEnrichment, error) {
	if len(providers) == 0 {
		return nil, nil
	}

	merged := &models.ProductEnrichment{
		Images: make([]string, 0),
	}

	for _, provider := range providers {
		enrichment, err := provider.EnrichProduct(ctx, item)
		if err != nil {
			return nil, err
		}
		mergeEnrichmentValues(merged, enrichment)
	}

	if merged.ReleaseDate == "" && merged.Stage == "" && merged.Features == "" && len(merged.Images) == 0 && merged.Description == "" {
		return nil, nil
	}

	return merged, nil
}

func mergeProductEnrichment(finalResult *models.FinalProductInfo, enrichment *models.ProductEnrichment) {
	if enrichment == nil {
		return
	}

	if finalResult.ReleaseDate == "" {
		finalResult.ReleaseDate = strings.TrimSpace(enrichment.ReleaseDate)
	}
	if finalResult.Stage == "" {
		finalResult.Stage = normalizeStage(enrichment.Stage)
	}
	if strings.TrimSpace(finalResult.Description) == "" && strings.TrimSpace(enrichment.Description) != "" {
		finalResult.Description = strings.TrimSpace(enrichment.Description)
	}

	if strings.TrimSpace(finalResult.Features) == "" {
		finalResult.Features = strings.TrimSpace(enrichment.Features)
	} else if strings.TrimSpace(enrichment.Features) != "" {
		finalResult.Features = mergeFeatureText(finalResult.Features, enrichment.Features)
	}
	finalResult.Images = uniqueStrings(append(finalResult.Images, enrichment.Images...))
}

func mergeEnrichmentValues(target, source *models.ProductEnrichment) {
	if source == nil {
		return
	}

	if target.ReleaseDate == "" {
		target.ReleaseDate = strings.TrimSpace(source.ReleaseDate)
	}
	if target.Stage == "" {
		target.Stage = normalizeStage(source.Stage)
	}
	if target.Description == "" {
		target.Description = strings.TrimSpace(source.Description)
	}

	if target.Features == "" {
		target.Features = strings.TrimSpace(source.Features)
	} else if strings.TrimSpace(source.Features) != "" {
		target.Features = mergeFeatureText(target.Features, source.Features)
	}
	target.Images = uniqueStrings(append(target.Images, source.Images...))
}

func applyFallbackFields(finalResult *models.FinalProductInfo, item models.UPCItem, patentData *models.PatentsViewResponse) {
	if len(finalResult.Images) == 0 {
		finalResult.Images = uniqueStrings(item.Images)
	}

	finalResult.Features = buildDetailedFeatures(finalResult, item)

	if finalResult.Stage == "" {
		finalResult.Stage = deriveStage(item, patentData)
	}

	finalResult.Features = strings.TrimSpace(finalResult.Features)
	finalResult.Images = uniqueStrings(finalResult.Images)
}

func matchesCompany(item models.UPCItem, company string) bool {
	target := normalizeSearchText(company)
	if target == "" {
		return true
	}

	candidates := []string{item.Brand, item.Title, item.Description}
	for _, candidate := range candidates {
		if strings.Contains(normalizeSearchText(candidate), target) {
			return true
		}
	}

	return false
}

func productDedupKey(item models.UPCItem) string {
	if strings.TrimSpace(item.UPC) != "" {
		return "upc:" + strings.TrimSpace(item.UPC)
	}
	if strings.TrimSpace(item.EAN) != "" {
		return "ean:" + strings.TrimSpace(item.EAN)
	}

	return normalizeSearchText(strings.Join([]string{item.Brand, item.Model, item.Title}, "|"))
}

func deriveStage(item models.UPCItem, patentData *models.PatentsViewResponse) string {
	if len(item.Offers) > 0 {
		return "marketed"
	}
	if patentData != nil && len(patentData.PatentFileWrapperDataBag) > 0 {
		return "development"
	}
	return ""
}

func normalizeStage(stage string) string {
	stage = strings.ToLower(strings.TrimSpace(stage))
	switch stage {
	case "", "n/a", "na", "unknown":
		return ""
	default:
		return stage
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func normalizeSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", " ", "_", " ", ",", " ", ".", " ", "/", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func buildDetailedFeatures(finalResult *models.FinalProductInfo, item models.UPCItem) string {
	parts := make([]string, 0, 4)

	if strings.TrimSpace(finalResult.Features) != "" {
		parts = append(parts, strings.TrimSpace(finalResult.Features))
	}

	description := strings.TrimSpace(finalResult.Description)
	if description == "" {
		description = strings.TrimSpace(item.Description)
	}
	if description != "" {
		parts = append(parts, description)
	}

	specSummary := buildSpecSummary(finalResult)
	if specSummary != "" {
		parts = append(parts, specSummary)
	}

	if strings.TrimSpace(finalResult.CoreInventionIdea) != "" {
		parts = append(parts, normalizeSentence("Core product idea: "+strings.TrimSpace(finalResult.CoreInventionIdea)))
	}

	return mergeFeatureText(parts...)
}

func buildSpecSummary(finalResult *models.FinalProductInfo) string {
	specParts := make([]string, 0, 5)

	if manufacturer := strings.TrimSpace(finalResult.CommercialDetails.Manufacturer); manufacturer != "" {
		specParts = append(specParts, "Manufacturer: "+manufacturer)
	}
	if model := strings.TrimSpace(finalResult.ProductIdentity.Model); model != "" {
		specParts = append(specParts, "Model: "+model)
	}

	orderedKeys := []string{"color", "size", "dimension", "weight", "patent_filing_date", "key_inventor"}
	labels := map[string]string{
		"color":              "Color",
		"size":               "Size",
		"dimension":          "Dimensions",
		"weight":             "Weight",
		"patent_filing_date": "Patent filing date",
		"key_inventor":       "Key inventor",
	}

	for _, key := range orderedKeys {
		if value := strings.TrimSpace(finalResult.TechSpecs[key]); value != "" {
			specParts = append(specParts, labels[key]+": "+value)
		}
	}

	if len(specParts) == 0 {
		return ""
	}

	return strings.Join(specParts, ". ") + "."
}

func mergeFeatureText(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))

	for _, part := range parts {
		normalized := normalizeParagraph(part)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, normalized)
	}

	return strings.Join(cleaned, "\n\n")
}

func joinFeatureParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}

	sentences := make([]string, 0, len(parts))
	for _, part := range uniqueStrings(parts) {
		normalized := normalizeSentence(part)
		if normalized != "" {
			sentences = append(sentences, normalized)
		}
	}

	return strings.Join(sentences, " ")
}

func normalizeParagraph(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	value = strings.Join(strings.Fields(value), " ")
	return value
}

func normalizeSentence(value string) string {
	value = normalizeParagraph(value)
	if value == "" {
		return ""
	}

	last := value[len(value)-1]
	if last != '.' && last != '!' && last != '?' {
		value += "."
	}

	return value
}
