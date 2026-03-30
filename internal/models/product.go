package models

// 1. Normalized data returned to the client.
type FinalProductInfo struct {
	ProductID         string            `json:"product_id"`
	ProductIdentity   ProductIdentity   `json:"product_identity"`
	CommercialDetails CommercialInfo    `json:"commercial_details"` // Thêm cục này vào
	Description       string            `json:"product_description"`
	CoreInventionIdea string            `json:"core_invention_idea"`
	ReleaseDate       string            `json:"release_date,omitempty"`
	Stage             string            `json:"stage,omitempty"`
	Features          string            `json:"features,omitempty"`
	Images            []string          `json:"images,omitempty"`
	TechSpecs         map[string]string `json:"technical_specifications"`
	IPAnalysis        IPAnalysisData    `json:"intellectual_property_analysis"`
	Vector            []float32         `json:"-"`
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

// 2. Structs for raw data returned by UPCitemdb.
type UPCItemDBResponse struct {
	Code   string    `json:"code"`
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Items  []UPCItem `json:"items"`
}

type UPCItem struct {
	EAN                  string   `json:"ean"`
	UPC                  string   `json:"upc"`
	GTIN                 string   `json:"gtin"`
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Brand                string   `json:"brand"`
	Model                string   `json:"model"`
	Color                string   `json:"color"`
	Size                 string   `json:"size"`
	Dimension            string   `json:"dimension"`
	Weight               string   `json:"weight"`
	Category             string   `json:"category"`
	Currency             string   `json:"currency"`
	LowestRecordedPrice  float64  `json:"lowest_recorded_price"`
	HighestRecordedPrice float64  `json:"highest_recorded_price"`
	Images               []string `json:"images"`
	Offers               []Offer  `json:"offers"`
}

type UPCResponse = UPCItemDBResponse

type Offer struct {
	Merchant     string  `json:"merchant"`
	Domain       string  `json:"domain"`
	Title        string  `json:"title"`
	Price        float64 `json:"price"`
	Currency     string  `json:"currency"`
	Shipping     string  `json:"shipping"`
	Condition    string  `json:"condition"`
	Availability string  `json:"availability"`
	Link         string  `json:"link"`
	UpdatedAt    int64   `json:"updated_at"`
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

type ProductEnrichment struct {
	ReleaseDate string   `json:"release_date,omitempty"`
	Stage       string   `json:"stage,omitempty"`
	Features    string   `json:"features,omitempty"`
	Images      []string `json:"images,omitempty"`
	Description string   `json:"description,omitempty"`
}

type CompanyProductsResponse struct {
	Company  string             `json:"company"`
	Total    int                `json:"total"`
	Offset   int                `json:"offset"`
	Limit    int                `json:"limit"`
	Products []FinalProductInfo `json:"products"`
}

type SerpAPIShoppingResponse struct {
	ShoppingResults []SerpAPIShoppingResult `json:"shopping_results"`
}

type SerpAPIShoppingResult struct {
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet"`
	Thumbnail  string   `json:"thumbnail"`
	Source     string   `json:"source"`
	Extensions []string `json:"extensions"`
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
