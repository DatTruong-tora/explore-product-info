# Product Insight API Service Report

## 1. Executive Summary

`product-insight-api` is a Go-based orchestration service that accepts a UPC code, retrieves product data from `UPCitemdb`, retrieves patent application data from the `USPTO` API, and then uses `Gemini` to enrich the result with higher-level analysis. The final output is a single normalized JSON response designed for downstream consumption.

The key design idea in the current implementation is a hybrid pipeline:

- deterministic mapping for fields that can be trusted directly from upstream APIs
- AI-based synthesis for fields that require interpretation, summarization, or reasoning

This design allows the service to stay reliable for structured fields such as identity, pricing, and marketplace links, while also generating more advanced IP-oriented insights from patent data.

## 2. Objective of the Service

The service solves a multi-source data problem. A single external source is not enough to generate a complete product insight report:

- `UPCitemdb` provides product-facing commercial data
- `USPTO` provides legal and technical patent application data
- `Gemini` turns raw or incomplete text into a cleaner analysis layer

The service therefore acts as an aggregation and enrichment layer that:

1. gets raw product data
2. gets patent-related data
3. analyzes the data
4. merges the results into one response payload

## 3. Current Project Structure

The current codebase is compact and divided into a few focused parts:

- `cmd/api/main.go`
  Application entry point. Loads environment variables, creates the Gin router, registers routes, and starts the server.

- `internal/handlers/product_handler.go`
  HTTP layer. Validates the incoming request and delegates all business logic to the service layer.

- `internal/services/aggregator.go`
  Core orchestration layer. Fetches external data, performs manual mapping, calls Gemini, and merges all outputs into the final response.

- `internal/models/product.go`
  Shared data contracts for:
  - normalized API output
  - UPC upstream response
  - USPTO upstream response
  - Gemini response envelope
  - LLM analysis result

- `.air.toml`
  Local live-reload development configuration.

- `go.mod` / `go.sum`
  Go module definition and dependency lock information.

## 4. Technologies Used and Why They Were Chosen

### 4.1 Go

Go is a strong fit for this service because:

- the application is network-heavy and IO-bound
- Go has excellent built-in support for HTTP clients and servers
- `context.Context` gives clean timeout and cancellation control
- the concurrency model is simple and effective for service orchestration
- the compiled binary is lightweight and easy to run locally

### 4.2 Gin

Gin is used as the HTTP framework because it offers:

- a lightweight routing layer
- easy JSON responses
- built-in middleware for logging and panic recovery
- simple route grouping for versioned APIs

This is appropriate for the current scope because the service currently exposes a small API surface and does not need a more complex framework.

### 4.3 godotenv

`godotenv` is used to load local configuration from `.env`. This is useful because the service depends on external API keys and local development is simpler when those keys can be loaded automatically.

### 4.4 UPCitemdb

This API is used as the primary product lookup source. It provides:

- product title
- brand
- model
- offers
- merchant links
- prices
- currency
- raw product description

This data is used as the commercial and identity foundation of the final response.

### 4.5 USPTO API

The USPTO API is used to retrieve patent application data based on the product brand. It provides:

- application records
- filing metadata
- inventor metadata
- applicant metadata
- correspondence address metadata

This data is important because it extends the service beyond basic product lookup and enables intellectual property analysis.

### 4.6 Gemini

Gemini is used for the analytical layer rather than the raw data retrieval layer. In the current version, Gemini is responsible for:

- polishing and completing the product description
- summarizing the core invention idea
- generating intellectual property analysis
- building claim chart style mappings


## 5. API Surface

The service currently exposes one endpoint:

- `GET /api/v1/product?upc=<UPC_CODE>`

Expected behavior:

- if `upc` is missing, the API returns `400`
- if any part of the aggregation pipeline fails, the API returns `500`
- if the flow succeeds, the API returns `200` with `status: "success"`

## 6. End-to-End Service Flow

This section describes the complete request flow from entry to final response.

### 6.1 Application Startup

When the service starts:

1. `main()` loads `.env` using `godotenv`.
2. A default Gin router is created.
3. Route group `/api/v1` is created.
4. `GET /product` is mapped to `handlers.GetProductInfo`.
5. The server starts on port `8080`.

### 6.2 Request Reception

The request enters `GetProductInfo(c *gin.Context)`.

The handler performs only HTTP responsibilities:

1. read the `upc` query parameter
2. validate that it exists
3. call `services.AggregateProductData(upcCode)`
4. transform service success or failure into an HTTP response

This separation is good because it keeps the handler thin and pushes business logic into the service layer.

### 6.3 Orchestration Begins

Inside `AggregateProductData(upcCode string)`:

1. a request-scoped timeout context is created with `45 seconds`
2. the service begins the product lookup phase
3. the returned data becomes the base object for the final result

This function is the heart of the service. It is where retrieval, analysis, and merging are coordinated.

## 7. How the Service Gets Data

The service gets data from two external systems before involving the LLM.

### 7.1 Step One: Product Retrieval from UPCitemdb

Function used:

- `fetchSeedData(ctx, upc string)`

How it works:

1. It builds a GET request to:
   `https://api.upcitemdb.com/prod/trial/lookup?upc=<UPC>`
2. It sends the request with a shared HTTP client.
3. It rejects any non-200 response.
4. It decodes the JSON body into `models.UPCResponse`.

What the service takes from this response:

- `title`
- `brand`
- `model`
- `description`
- `offers`

Why this matters:

- it provides the product identity
- it provides commercial offer data for marketplaces and pricing
- it provides the raw description that Gemini later refines

### 7.2 Step Two: Patent Retrieval from USPTO

Function used:

- `fetchPatentData(ctx, brand string)`

How it works:

1. It reads `USPTO_API_KEY` from the environment.
2. It builds a USPTO search query using the product brand:
   `assignee:"<brand>"`
3. It creates a GET request to the USPTO applications search endpoint.
4. It sets the required headers:
   - `Accept: application/json`
   - `X-API-Key: <USPTO_API_KEY>`
5. It validates the response status code.
6. It decodes the response into `models.PatentsViewResponse`.

What the service takes from this response:

- `filingDate`
- `firstInventorName`
- `applicantNameText`
- `countryName`
- raw patent/application records for Gemini analysis

Why this matters:

- it provides legal and technical metadata not available in UPC data
- it gives the service a basis for inferring manufacturer and country information
- it gives Gemini context for the IP analysis portion of the final response

## 8. How the Service Analyzes Data

The service uses two different analysis strategies:

- manual analysis and mapping in Go
- AI analysis through Gemini

### 8.1 Manual Analysis in Go

The current service intentionally performs direct mapping for fields that can be deterministically extracted.

This includes:

- product identity
- commercial marketplaces
- average price
- manufacturer fallback
- country of origin
- patent filing date
- key inventor

The important point is that these fields are not delegated to the LLM unless interpretation is actually needed.

### 8.2 LLM Analysis Through Gemini

Function used:

- `synthesizeWithLLM(ctx, description, patentData)`

How it works:

1. It reads and sanitizes `GEMINI_API_KEY`.
2. It serializes the patent response into JSON.
3. It builds a prompt instructing Gemini to:
   - read the raw product description
   - gracefully complete it if it is truncated
   - rewrite it as a polished paragraph
   - summarize the core invention idea
   - generate associated patents and claim-chart style mappings
4. It calls:
   `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent`
5. It forces a JSON-only response.
6. It validates the HTTP response.
7. It decodes the Gemini response envelope.
8. It extracts the text payload.
9. It parses that JSON into `models.LLMAnalysisResult`.

What Gemini currently produces:

- `polished_description`
- `core_invention_idea`
- `intellectual_property_analysis`

This is an important architectural change from a fully AI-composed result. In the current version, Gemini is no longer responsible for the entire response. It is responsible only for the interpretation-heavy fields.

## 9. How the Service Merges Data

The current implementation follows a layered merge strategy.

### 9.1 Base Response Initialization

After the UPC response is returned, the service initializes `FinalProductInfo` with:

- a generated `product_id`
- `product_identity` from UPC data
- the raw UPC description as the temporary description
- an empty `technical_specifications` map
- `commercial_details.manufacturer` set to the product brand as the initial fallback
- an empty `marketplaces` list

This is the first merge stage: create a stable response skeleton from the most reliable product-facing source.

### 9.2 Commercial Merge from UPC Offers

The service then iterates through `item.Offers` and maps each offer into `models.Marketplace`.

For each offer it captures:

- merchant name
- price
- currency
- product link

At the same time, it computes:

- total price
- offer count
- average price

This is a strong design choice because price computation remains deterministic and transparent in Go rather than being inferred by the LLM.

### 9.3 Patent Metadata Merge

After the USPTO response is retrieved, the service walks through the patent records to find the first usable values for:

- `patent_filing_date`
- `key_inventor`
- `manufacturer`
- `country_of_origin`

Important behavior in this stage:

- manufacturer is initially set from UPC brand
- manufacturer is overwritten if a more formal applicant name is found in USPTO data
- country is pulled from correspondence address information
- the loop stops early once all desired fields have been found

This is the second merge stage: enrich deterministic fields using legal metadata.

### 9.4 LLM Merge

After deterministic mapping is complete, Gemini is called.

The returned LLM fields are merged into the existing response:

- `finalResult.Description = llmAnalysis.PolishedDescription`
- `finalResult.CoreInventionIdea = llmAnalysis.CoreInventionIdea`
- `finalResult.IPAnalysis = llmAnalysis.IPAnalysis`

This is the third merge stage: overwrite or fill the interpretation-heavy fields with AI-generated analysis while preserving the deterministic fields already built by Go.

## 10. Why the Current Hybrid Approach Is Strong

The current architecture is stronger than a purely manual mapper or a purely LLM-generated response.

### 10.1 Why Not Manual Mapping Only

Manual mapping alone is reliable for:

- identity fields
- pricing fields
- marketplace fields
- patent metadata fields

However, manual mapping is weak at:

- polishing incomplete descriptions
- summarizing invention concepts
- producing claim-chart style analysis

### 10.2 Why Not LLM for Everything

Using the LLM for all fields would create unnecessary risk:

- pricing might be hallucinated
- links might be omitted or reformatted
- brand or model values might drift
- structured values could become inconsistent

### 10.3 Why the Combination Works

The current design divides responsibilities in a sensible way:

- Go handles retrieval, validation, mapping, and arithmetic
- external APIs provide authoritative source data
- Gemini handles interpretation, completion, and synthesis

That means the service is:

- structured where it should be structured
- flexible where it needs inference
- easier to reason about when debugging

## 11. Output Data Model

The final response model is defined by `models.FinalProductInfo`.

Current top-level output fields:

- `product_id`
- `product_identity`
- `commercial_details`
- `product_description`
- `core_invention_idea`
- `technical_specifications`
- `intellectual_property_analysis`

### 11.1 Product Identity

Contains:

- `title`
- `brand`
- `model`
- `upc`

### 11.2 Commercial Details

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

### 11.3 Technical Specifications

The current implementation fills this map with patent-related metadata such as:

- `patent_filing_date`
- `key_inventor`

This map can scale later if more structured technical fields are introduced.

### 11.4 Intellectual Property Analysis

Contains:

- `associated_patents`
- `claim_charts`

Each claim chart includes:

- `patent_claim`
- `product_feature_mapped`
- `invalidity_search_relevance`

## 12. Concurrency and Control Flow Notes

The patent fetch step is currently launched inside a goroutine with a `WaitGroup`, then awaited immediately.

This means:

- the code is already shaped for possible future parallel expansion
- the current implementation still behaves effectively as sequential orchestration

The service also uses:

- one shared HTTP client with a `60s` timeout
- one request context with a `45s` timeout for the whole aggregation

This provides basic protection against hanging upstream requests.

## 13. Error Handling Approach

The service currently handles failures in a pragmatic way:

- missing `upc` returns `400`
- failed external calls return `500`
- missing environment keys cause explicit errors
- non-200 responses from USPTO or Gemini include useful error details
- empty Gemini candidate results are rejected

This is appropriate for a prototype or early service, although a production version would likely add typed error categories and more structured logging.

## 14. Current Limitations

The following limitations are visible in the current code:

- the service assumes the first UPC result is the correct product
- USPTO lookup uses only the brand as search input, which may be noisy
- if USPTO returns weak data, manufacturer and country may remain incomplete
- Gemini output quality still depends on prompt quality and upstream data quality
- the patent goroutine is not yet delivering real concurrency gains
- there are no automated tests in the current repository snapshot
- there is no caching for repeated UPC lookups
- there is no retry strategy for transient upstream failures


## 15. How To Run the Service

### 15.1 Prerequisites

Before running the service, ensure:

- the required API keys are available

Required environment variables:

- `GEMINI_API_KEY`
- `USPTO_API_KEY`

The application attempts to load these from `.env` at startup. If `.env` is missing, it falls back to system environment variables.

### 15.2 Install Dependencies

Run:

```powershell
go mod download
```

### 15.3 Run Normally

Run the API directly with Go:

```powershell
go run cmd/api/main.go
```

Expected startup message:

```text
Server is running at http://localhost:8080
```

### 15.4 Run With Live Reload

This project also includes `.air.toml`, so it can be run with `air` for automatic rebuilds during development.

If `air` is installed, run:

```powershell
air
```

Based on the current configuration:

- the build output is written to `tmp/main.exe`
- the build command is:
  `go build -o ./tmp/main.exe ./cmd/api/main.go`
- Go, template, and HTML file changes are watched

### 15.5 Test the Endpoint

Once the server is running, call:

```powershell
curl "http://localhost:8080/api/v1/product?upc=9780593486634"
```

Or in a browser/Postman:

```text
http://localhost:8080/api/v1/product?upc=9780593486634
```

## 16. Example Runtime Flow

This is the practical execution sequence for one request:

1. The client sends `GET /api/v1/product?upc=<value>`.
2. The handler validates that `upc` is present.
3. The service requests product data from `UPCitemdb`.
4. The first UPC item is used as the product baseline.
5. The service maps identity and offer data into the response object.
6. The service requests patent application data from `USPTO` using the brand.
7. The service maps filing date, inventor, manufacturer, and country when available.
8. The service sends the raw description and patent data to `Gemini`.
9. Gemini returns a JSON analysis payload.
10. The service merges the polished description, invention idea, and IP analysis into the existing response.
11. The handler returns the unified JSON payload.

## 17. API Response Paste Area

Use the section below if you want to attach live output when sending the report.

### 17.1 Request Used

```text
GET /api/v1/product?upc=9780593486634
```

### 17.2 Final API Response Returned by This Service

```json
{
    "data": {
        "product_id": "PRD-4756d3f84460bccfbbb405d31416824c",
        "product_identity": {
            "title": "Mrs. Peanuckle's Earth Alphabet - (Mrs. Peanuckle's Alphabet) by Mrs Peanuckle (Board Book)",
            "brand": "",
            "model": "",
            "upc": "9780593486634"
        },
        "commercial_details": {
            "manufacturer": "Stratasys, Inc.",
            "country_of_origin": "UNITED STATES",
            "average_price": 59.51285714285715,
            "marketplaces": [
                {
                    "store_name": "Kohl's",
                    "price": 8.99,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2r2x2z2z2637474q2&tid=3&seq=1774337509&plt=75a7aba4d50953ff05ab39a3dc08b0e5"
                },
                {
                    "store_name": "eCampus.com",
                    "price": 6.74,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2q253131353c444w2&tid=3&seq=1774337509&plt=b9bc9e2efe681f3cc1abd659db3653f7"
                },
                {
                    "store_name": "UnbeatableSale.com",
                    "price": 12.49,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2r223w213y284b4x2&tid=3&seq=1774337509&plt=7306e0dbc3d68ba5933c7f2eaf892155"
                },
                {
                    "store_name": "BiggerBooks.com",
                    "price": 6.94,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2q25323v223e454z2&tid=3&seq=1774337509&plt=d9e2954ebb7278f8921f899ccf446ae9"
                },
                {
                    "store_name": "Rakuten(Buy.com)",
                    "price": 366.44,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=u2x20323y233f474q2&tid=3&seq=1774337509&plt=ae725d18ef50e279b8df0de3fa1de531"
                },
                {
                    "store_name": "Target",
                    "price": 7,
                    "currency": "",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2q243w23363b4d4u2&tid=3&seq=1774337509&plt=96d620f6abe1ad6df91da52136ee60d4"
                },
                {
                    "store_name": "VitalSource ",
                    "price": 7.99,
                    "currency": "ZAR",
                    "product_link": "https://www.upcitemdb.com/norob/alink/?id=v2q2532313y29494t2&tid=3&seq=1774337509&plt=73eb6f36159fe89372228deb3c4e66f3"
                }
            ]
        },
        "product_description": "The product description was not provided, therefore a detailed analysis of specific product features and a completion of any cut-off sentences cannot be performed. This significantly limits the ability to precisely identify the product's market position or unique selling propositions.",
        "core_invention_idea": "Without a product description or specific product features, it is impossible to determine a core invention idea. The provided patent data covers an extremely wide array of disparate technologies including cooking devices, additive manufacturing molds, shock absorbers, deep learning, LiDAR systems, adjustable bassinets, tailgate deactivation systems, energy harvesting, and various communication technologies. This diverse set of patents does not point to a single core invention for a specific product.",
        "technical_specifications": {
            "key_inventor": "12426739",
            "patent_filing_date": "2026-03-20"
        },
        "intellectual_property_analysis": {
            "associated_patents": [
                "90016067",
                "19573211",
                "90016068",
                "19115961",
                "90016065",
                "90016063",
                "90016066",
                "19572431",
                "19571835",
                "19571386",
                "90016062",
                "90016059",
                "19570818",
                "90016052",
                "90016058",
                "90016057",
                "18463811",
                "19059560",
                "90016055",
                "90016056",
                "90016051",
                "90016050",
                "90016048",
                "19140425",
                "90016054"
            ],
            "claim_charts": [
                {
                    "patent_claim": "COOKING DEVICES AND COMPONENTS THEREOF (from application 90016067)",
                    "product_feature_mapped": "Product description and features are not provided, preventing any meaningful mapping.",
                    "invalidity_search_relevance": "Cannot assess without product features."
                },
                {
                    "patent_claim": "SACRIFICIAL ADDITIVELY MANUFACTURED MOLDS FOR USE IN INJECTION MOLDING PROCESSES (from application 19573211)",
                    "product_feature_mapped": "Product description and features are not provided, preventing any meaningful mapping.",
                    "invalidity_search_relevance": "Cannot assess without product features."
                },
                {
                    "patent_claim": "DEPTH-ADJUSTABLE BASSINET (from application 19571386)",
                    "product_feature_mapped": "Product description and features are not provided, preventing any meaningful mapping.",
                    "invalidity_search_relevance": "Cannot assess without product features."
                },
                {
                    "patent_claim": "ACCELERATED DEEP LEARNING (from application 19572431)",
                    "product_feature_mapped": "Product description and features are not provided, preventing any meaningful mapping.",
                    "invalidity_search_relevance": "Cannot assess without product features."
                }
            ]
        }
    },
    "status": "success"
}
```

## 18. Conclusion

The current version of `product-insight-api` is best described as a hybrid enrichment service. It does not simply proxy third-party APIs and it does not delegate the entire result to AI. Instead, it retrieves authoritative source data, maps the deterministic fields in Go, uses Gemini only for interpretation-heavy analysis, and then merges all pieces into a single normalized response.

From an engineering perspective, this is a sensible architecture for the problem being solved. It keeps control over structured data while still benefiting from the reasoning strengths of an LLM. As a result, the service is easier to maintain, easier to debug, and more trustworthy than an all-LLM pipeline, while still being more capable than a strictly hard-coded mapper.
