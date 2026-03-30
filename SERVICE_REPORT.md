# Product Insight API Service Report

## 1. Executive Summary

`product-insight-api` is a Go-based aggregation service that now supports two product lookup flows:

- lookup of a single product by UPC
- lookup of multiple products by company name

The service combines structured product data from `UPCitemdb`, patent application data from `USPTO`, optional catalog-style enrichment from `SerpAPI`, and interpretation-heavy analysis from `Gemini`.

The current design is intentionally hybrid:

- deterministic mapping in Go for trusted identity, marketplace, price, image, and specification fields
- AI-based synthesis only for fields that benefit from interpretation, such as the polished description, core invention idea, and IP analysis

This version also extends the public response model with richer product information:

- `release_date`
- `stage`
- `features`
- `images`

At the same time, it preserves the previous normalized response structure so existing clients can continue to consume the core product fields.

## 2. Objective of the Service

The service solves a multi-source enrichment problem:

- `UPCitemdb` provides product catalog, offers, images, and search capability
- `USPTO` provides patent and assignee metadata
- `SerpAPI` can optionally provide additional product snippets, images, and commercial descriptors
- `Gemini` polishes incomplete text and generates interpretation-heavy IP analysis

The service acts as a normalized aggregation layer that:

1. retrieves raw product data
2. enriches it with legal, catalog, and descriptive context
3. synthesizes selected analysis fields
4. returns a stable JSON response for frontend consumption

## 3. Current Project Structure

The current codebase is divided into focused layers:

- `cmd/api/main.go`  
  Application entry point. Loads environment variables, creates the Gin router, registers routes, and starts the server.

- `internal/handlers/product_handler.go`  
  HTTP layer. Validates query parameters and delegates business logic to the service layer.

- `internal/services/aggregator.go`  
  Shared infrastructure utilities for upstream API calls, Gemini synthesis, Milvus search, and embedding persistence helpers.

- `internal/services/catalog_service.go`  
  Main product aggregation pipeline for both UPC-based lookup and company-based lookup. Builds the normalized result, merges enrichment, applies fallback rules, and assembles final product payloads.

- `internal/services/providers.go`  
  Provider abstraction layer. Defines discovery and enrichment providers and implements:
  - `UPCitemdb` product discovery
  - optional `SerpAPI` enrichment

- `internal/models/product.go`  
  Shared data contracts for:
  - normalized API output
  - UPC upstream response
  - USPTO upstream response
  - Gemini response envelope
  - LLM analysis result
  - company-products response

- `internal/services/catalog_service_test.go`
- `internal/services/providers_test.go`  
  Automated tests for the current service layer behavior.

## 4. Technologies Used and Why They Were Chosen
### 4.1 UPCitemdb

`UPCitemdb` is the primary discovery source. It is used for:

- UPC lookup
- company-based product search
- product identity fields
- offer and marketplace information
- average price inputs
- images
- base descriptions
- lightweight specs such as color, size, dimensions, and weight

### 4.2 USPTO API

The `USPTO` API is used to enrich products with patent application context based on brand or assignee information. It contributes:

- filing dates
- inventor data
- applicant data
- country metadata
- raw patent records for Gemini analysis

### 4.3 SerpAPI

`SerpAPI` is an optional secondary enrichment provider, enabled when `SERPAPI_API_KEY` is present.

It is used to supplement:

- image URLs
- descriptive snippets
- feature-like extensions from shopping results

### 4.4 Gemini

`Gemini` is used only for interpretation-heavy fields. In the current implementation it is responsible for:

- polishing and completing the product description
- summarizing the core invention idea
- generating intellectual property analysis
- building claim-chart style mappings

## 5. API Surface

The service currently exposes three endpoints:

- `GET /api/v1/product?upc=<UPC_CODE>`
- `GET /api/v1/products-by-company?company=<COMPANY>&limit=<N>&offset=<N>`
- `GET /api/v1/search?query=<TEXT>&limit=<N>&min_score=<FLOAT>`

### 5.1 Single Product by UPC

`GET /api/v1/product?upc=<UPC_CODE>`

Expected behavior:

- missing `upc` returns `400`
- fatal aggregation failures return `500`
- success returns `200` with `status: "success"` and one `FinalProductInfo`

### 5.2 Products by Company

`GET /api/v1/products-by-company?company=<COMPANY>&limit=<N>&offset=<N>`

Expected behavior:

- missing `company` returns `400`
- invalid `limit` or `offset` returns `400`
- success returns `200` with `status: "success"` and `data.products`

Behavior notes:

- `limit` defaults to `5`
- `limit` is capped at `10`
- `offset` defaults to `0`
- duplicate products are filtered by UPC, EAN, or normalized identity
- upstream `404` from UPC search is handled gracefully and returned as an empty list rather than a server error

### 5.3 Vector Search

`GET /api/v1/search?query=<TEXT>&limit=<N>&min_score=<FLOAT>`

This endpoint searches Milvus embeddings for previously persisted product vectors.

## 6. End-to-End Service Flow

### 6.1 Application Startup

On startup:

1. `main()` loads `.env`
2. Gin router is created
3. rate-limiting middleware is attached
4. `/api/v1` routes are registered
5. the server starts on port `8080`

### 6.2 Request Reception

The handlers are intentionally thin:

- `GetProductInfo` validates `upc` and calls `services.AggregateProductData`
- `GetCompanyProducts` validates `company`, `limit`, and `offset`, then calls `services.AggregateCompanyProducts`
- `SearchProducts` validates vector-search parameters and calls `services.SearchProducts`

### 6.3 Orchestration Entry Points

The main service entry points are:

- `AggregateProductData(upcCode string)`
- `AggregateCompanyProducts(company string, limit, offset int)`

Both flows share the same normalized product-building pipeline through:

- `aggregateProductFromItem(...)`

## 7. How the Service Gets Data

### 7.1 Product Retrieval from UPCitemdb

Single UPC lookup uses:

- `fetchSeedData(ctx, upc string)`

It calls:

- `https://api.upcitemdb.com/prod/trial/lookup?upc=<UPC>`

Company-based search uses:

- `upcItemDBProvider.SearchByCompany(...)`

It first tries a brand-aware search strategy:

- `s=<company>&brand=<company>`

If that returns `404`, it falls back to:

- `s=<company>`

If both return `404`, the service returns an empty product list rather than failing the request.

### 7.2 Patent Retrieval from USPTO

Patent enrichment uses:

- `fetchPatentData(ctx, brand string)`

It:

1. reads `USPTO_API_KEY`
2. builds search text as `assignee:"<brand>"`
3. calls the USPTO application search endpoint
4. decodes the response into `models.PatentsViewResponse`

This data is used both for deterministic metadata mapping and as context for Gemini.

### 7.3 Optional Enrichment from SerpAPI

Optional secondary product enrichment uses:

- `serpAPIProvider.EnrichProduct(...)`

It:

1. reads `SERPAPI_API_KEY`
2. builds a Google Shopping query from brand, title, and model
3. extracts:
   - thumbnails
   - snippets
   - shopping result extensions

This enrichment is best-effort and does not block the core response if unavailable.

## 8. How the Service Analyzes Data

The service uses two analysis strategies:

- deterministic mapping in Go
- AI-based synthesis through Gemini

### 8.1 Deterministic Mapping in Go

Fields handled directly in Go include:

- product identity
- marketplaces
- average price
- manufacturer fallback
- country of origin
- patent filing date
- key inventor
- lightweight technical specs
- images
- company-search deduplication
- stage fallback

### 8.2 LLM Analysis Through Gemini

Gemini is invoked by:

- `synthesizeWithLLM(ctx, description, patentData)`

Gemini currently produces:

- `polished_description`
- `core_invention_idea`
- `intellectual_property_analysis`

In company-based aggregation, Gemini failures are treated leniently and logged so one weak upstream product does not fail the entire company response.

## 9. How the Service Merges Data

The current implementation follows a layered merge strategy.

### 9.1 Base Product Initialization

`newBaseProductInfo(item)` creates a stable response skeleton with:

- generated `product_id`
- `product_identity`
- raw description
- `commercial_details`
- `technical_specifications`
- `images`

It also computes `average_price` from `offers`.

### 9.2 Patent Metadata Merge

`mapPatentData(...)` enriches deterministic fields from USPTO:

- `patent_filing_date`
- `key_inventor`
- `manufacturer`
- `country_of_origin`

### 9.3 Provider Enrichment Merge

`enrichProduct(...)` and `mergeProductEnrichment(...)` merge optional fields from secondary providers:

- `release_date`
- `stage`
- `features`
- `images`
- descriptive text

### 9.4 LLM Merge

If Gemini succeeds, the service merges:

- polished description
- core invention idea
- IP analysis

### 9.5 Fallback Rules

`applyFallbackFields(...)` applies deterministic fallbacks:

- `images` fall back to UPC-provided images
- `features` are rebuilt into one detailed string using:
  - provider features
  - polished or raw description
  - spec summary
  - core invention idea
- `stage` falls back to:
  - `marketed` when offers exist
  - `development` when patent data exists but offers do not

## 10. Output Data Model

The public product response is defined by `models.FinalProductInfo`.

Current top-level output fields:

- `product_id`
- `product_identity`
- `commercial_details`
- `product_description`
- `core_invention_idea`
- `release_date`
- `stage`
- `features`
- `images`
- `technical_specifications`
- `intellectual_property_analysis`

Important note:

- `vector` still exists internally in Go, but is no longer serialized in API responses because it is tagged with `json:"-"`

### 10.1 Product Identity

Contains:

- `title`
- `brand`
- `model`
- `upc`
- `category`

### 10.2 Commercial Details

Contains:

- `manufacturer`
- `country_of_origin`
- `average_price`
- `marketplaces`

Each marketplace record contains:

- `store_name`
- `price`
- `currency`
- `product_link`

### 10.3 Features and Images

New enrichment fields:

- `features` is a single detailed string, not an array
- `images` is an array of product image URLs
- `release_date` is optional and only set when upstream enrichment provides it
- `stage` is optional and may be derived heuristically

### 10.4 Technical Specifications

The `technical_specifications` map currently includes values such as:

- `color`
- `size`
- `dimension`
- `weight`
- `patent_filing_date`
- `key_inventor`

### 10.5 Intellectual Property Analysis

Contains:

- `associated_patents`
- `claim_charts`

Each claim chart contains:

- `patent_claim`
- `product_feature_mapped`
- `invalidity_search_relevance`

### 10.6 Company Products Response

The company endpoint returns `models.CompanyProductsResponse`:

- `company`
- `total`
- `offset`
- `limit`
- `products`

Each entry inside `products` is a full `FinalProductInfo`.

## 11. Search and Persistence Notes

The Milvus vector-search path is still present for semantic search:

- embeddings are generated through Gemini embedding APIs
- `SearchProducts(...)` queries Milvus

However, the `vector` field is not returned to clients anymore.

The current `catalog_service.go` flow does not actively persist embeddings because the persistence call is commented out in the shared aggregation pipeline. The helper functions still exist in `aggregator.go`, but the public product response no longer exposes vectors.

## 12. Concurrency, Timeouts, and Control Flow

Current timeout strategy:

- UPC and external HTTP calls share one `60s` HTTP client timeout
- single-product aggregation uses a `45s` request context
- company aggregation uses a `75s` request context
- background embedding persistence helpers use a longer timeout if enabled

The current company pipeline processes products sequentially and skips individual product failures rather than failing the whole company request.

## 13. Error Handling Approach

The service currently handles failures as follows:

- missing query parameters return `400`
- invalid `limit`, `offset`, or `min_score` return `400`
- fatal UPC lookup failures return `500`
- upstream `404` during company search is treated as an empty result set
- USPTO failures are logged and treated as soft failures
- SerpAPI failures are logged and treated as soft failures
- Gemini failures are:
  - fatal for strict UPC-based aggregation
  - soft failures for lenient company-based aggregation

This creates a more resilient company-search flow than the earlier single-path implementation.

## 14. Current Limitations

The following limitations remain:

- UPC-based single lookup still assumes the first UPC result is the correct item
- USPTO search is based on brand/assignee text and can be noisy
- `release_date` is only populated when an enrichment source provides it
- `stage` can be heuristic rather than authoritative
- company search quality depends on upstream product indexing quality
- some provider fields such as manufacturer can still be noisy when upstream data is weak
- there is no cache layer yet for repeated upstream lookups
- the company enrichment pipeline is currently sequential

## 15. How To Run the Service

### 15.1 Required Environment Variables

Required for core functionality:

- `GEMINI_API_KEY`
- `USPTO_API_KEY`

Optional depending on features used:

- `SERPAPI_API_KEY`
- `MILVUS_ADDRESS`

The application loads `.env` when present, otherwise it falls back to system environment variables.

### 15.2 Install Dependencies

```powershell
go mod download
```

### 15.3 Run Normally

```powershell
go run cmd/api/main.go
```

Expected startup message:

```text
Server is running at http://localhost:8080
```

### 15.4 Run With Live Reload

If `air` is installed:

```powershell
air
```

## 16. Example Requests

### 16.1 Single Product by UPC

```text
GET /api/v1/product?upc=9780593486634
```

### 16.2 Products by Company

```text
GET /api/v1/products-by-company?company=Apple&limit=3
```

### 16.3 Vector Search

```text
GET /api/v1/search?query=tablet&limit=5&min_score=0.6
```

## 17. Example Response Shapes

### 17.1 Single Product Response

```json
{
  "status": "success",
  "data": {
    "product_id": "PRD-...",
    "product_identity": {
      "title": "Example Product",
      "brand": "Example Brand",
      "model": "EX-100",
      "upc": "123456789012",
      "category": "Electronics"
    },
    "commercial_details": {
      "manufacturer": "Example Brand",
      "country_of_origin": "UNITED STATES",
      "average_price": 199.99,
      "marketplaces": [
        {
          "store_name": "Example Store",
          "price": 199.99,
          "currency": "USD",
          "product_link": "https://example.com/product"
        }
      ]
    },
    "product_description": "Polished product description...",
    "core_invention_idea": "High-level invention summary...",
    "release_date": "2025-05-12",
    "stage": "marketed",
    "features": "Detailed full-text feature summary...",
    "images": [
      "https://example.com/image-1.jpg",
      "https://example.com/image-2.jpg"
    ],
    "technical_specifications": {
      "color": "Black",
      "weight": "0.3 Pounds",
      "patent_filing_date": "2026-03-24",
      "key_inventor": "Jane Doe"
    },
    "intellectual_property_analysis": {
      "associated_patents": [
        "US12345678"
      ],
      "claim_charts": [
        {
          "patent_claim": "Example claim text",
          "product_feature_mapped": "Mapped feature text",
          "invalidity_search_relevance": "High"
        }
      ]
    }
  }
}
```

### 17.2 Company Products Response

```json
{
  "status": "success",
  "data": {
    "company": "Apple",
    "total": 9711,
    "offset": 0,
    "limit": 3,
    "products": [
      {
        "product_id": "PRD-...",
        "product_identity": {
          "title": "Example Product",
          "brand": "Apple",
          "model": "EX-100",
          "upc": "123456789012",
          "category": "Electronics"
        },
        "commercial_details": {
          "manufacturer": "Apple",
          "country_of_origin": "UNITED STATES",
          "average_price": 499.99,
          "marketplaces": []
        },
        "product_description": "Polished product description...",
        "core_invention_idea": "High-level invention summary...",
        "release_date": "2025-05-12",
        "stage": "marketed",
        "features": "Detailed full-text feature summary...",
        "images": [
          "https://example.com/image-1.jpg"
        ],
        "technical_specifications": {},
        "intellectual_property_analysis": {
          "associated_patents": [],
          "claim_charts": []
        }
      }
    ]
  }
}
```