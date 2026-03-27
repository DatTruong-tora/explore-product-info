package models

// 1. Normalized data returned to the client.
type FinalProductInfo struct {
	ProductID         string            `json:"product_id"`
	ProductIdentity   ProductIdentity   `json:"product_identity"`
	CommercialDetails CommercialInfo    `json:"commercial_details"` // Thêm cục này vào
	Description       string            `json:"product_description"`
	CoreInventionIdea string            `json:"core_invention_idea"`
	TechSpecs         map[string]string `json:"technical_specifications"`
	IPAnalysis        IPAnalysisData    `json:"intellectual_property_analysis"`
	Vector            []float32         `json:"vector"`
}

type CommercialInfo struct {
	Manufacturer    string        `json:"manufacturer"`
	CountryOfOrigin string        `json:"country_of_origin"`
	AveragePrice    float64       `json:"average_price"`
	Marketplaces    []Marketplace `json:"marketplaces"`
}

type Marketplace struct {
	StoreName   string  `json:"store_name"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency"`
	ProductLink string  `json:"product_link"`
}

type ProductIdentity struct {
	Title    string `json:"title"`
	Brand    string `json:"brand"`
	Model    string `json:"model"`
	UPC      string `json:"upc"`
	Category string `json:"category"`
}

type IPAnalysisData struct {
	AssociatedPatents []string     `json:"associated_patents"`
	ClaimCharts       []ClaimChart `json:"claim_charts"`
}

type ClaimChart struct {
	PatentClaim   string `json:"patent_claim"`
	MappedFeature string `json:"product_feature_mapped"`
	Relevance     string `json:"invalidity_search_relevance"`
}

// 2. Struct for raw data returned by UPCitemdb.
type UPCResponse struct {
	Items []struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Brand       string  `json:"brand"`
		Model       string  `json:"model"`
		Category    string  `json:"category"`
		Offers      []Offer `json:"offers"`
	} `json:"items"`
}

type Offer struct {
	Merchant string  `json:"merchant"`
	Domain   string  `json:"domain"`
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	Link     string  `json:"link"`
}

// 3. Struct for raw data returned by USPTO.
type PatentsViewResponse struct {
	Count                    int          `json:"count"`
	PatentFileWrapperDataBag []PatentItem `json:"patentFileWrapperDataBag"`
}

// Tách nhỏ Struct ra cho dễ quản lý và scale sau này
type PatentItem struct {
	ApplicationNumberText string              `json:"applicationNumberText"`
	ApplicationMetaData   ApplicationMetaData `json:"applicationMetaData"`
}

type ApplicationMetaData struct {
	InventionTitle    string         `json:"inventionTitle"`
	FilingDate        string         `json:"filingDate"`        // filing date
	FirstInventorName string         `json:"firstInventorName"` // first inventor name
	ApplicantBag      []ApplicantBag `json:"applicantBag"`      // applicant bag
}

type ApplicantBag struct {
	ApplicantNameText        string       `json:"applicantNameText"`
	CorrespondenceAddressBag []AddressBag `json:"correspondenceAddressBag"`
}

type AddressBag struct {
	CityName             string `json:"cityName"`
	GeographicRegionName string `json:"geographicRegionName"`
	CountryName          string `json:"countryName"`
}

// 4. Struct for raw data returned by the Gemini API.
type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// Struct for the LLM analysis result
type LLMAnalysisResult struct {
	PolishedDescription string         `json:"polished_description"`
	CoreInventionIdea   string         `json:"core_invention_idea"`
	IPAnalysis          IPAnalysisData `json:"intellectual_property_analysis"`
}

type SearchMatch struct {
	ProductID string  `json:"product_id"`
	Title     string  `json:"title"`
	Score     float32 `json:"score"`
}

type SearchResponse struct {
	Query    string        `json:"query"`
	Limit    int           `json:"limit"`
	MinScore float32       `json:"min_score"`
	Matches  []SearchMatch `json:"matches"`
}
