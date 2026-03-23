package models

// 1. Normalized data returned to the client.
type FinalProductInfo struct {
	ProductID         string            `json:"product_id"`
	ProductIdentity   ProductIdentity   `json:"product_identity"`
	Description       string            `json:"product_description"`
	Features          []string          `json:"product_features"`
	CoreInventionIdea string            `json:"core_invention_idea"`
	TechSpecs         map[string]string `json:"technical_specifications"`
	IPAnalysis        IPAnalysisData    `json:"intellectual_property_analysis"`
}

type ProductIdentity struct {
	Title string `json:"title"`
	Brand string `json:"brand"`
	Model string `json:"model"`
	UPC   string `json:"upc"`
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
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Brand       string   `json:"brand"`
		Model       string   `json:"model"`
		Features    []string `json:"features"` // The real API returns a features array.
	} `json:"items"`
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
	InventionTitle        string `json:"inventionTitle"`
	ApplicationStatusDate string `json:"applicationStatusDate"`
	FirstInventorName     string `json:"firstInventorName"`
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