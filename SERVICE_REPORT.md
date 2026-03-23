# Product Insight API Service Report

## 1. General Overview

`product-insight-api` is a small Go-based HTTP service that accepts a UPC code, fetches product data from an external UPC source, looks up patent-related information from the USPTO API using the product brand, and then sends both datasets to Gemini to synthesize a structured product-and-IP analysis response.

At a high level, the service acts as an orchestration layer between:

- A client calling the local API
- A product lookup API
- A patent search API
- A large language model used for synthesis

The main business goal is to transform raw external data into a normalized response that is easier for downstream consumers to use.

## 2. Project Structure

The current project is intentionally compact and organized into a few clear layers:

- `cmd/api/main.go`
  Starts the HTTP server, loads environment variables, and registers routes.

- `internal/handlers/product_handler.go`
  Handles the incoming HTTP request, validates the required query parameter, and returns JSON responses.

- `internal/services/aggregator.go`
  Contains the core orchestration logic:
  fetch product data, fetch patent data, call Gemini, and assemble the final response.

- `internal/models/product.go`
  Defines the response structures for:
  - the normalized API response
  - UPC source data
  - USPTO source data
  - Gemini response data

- `.env`
  Stores required runtime secrets locally:
  - `GEMINI_API_KEY`
  - `USPTO_API_KEY`

- `go.mod` / `go.sum`
  Define the Go module and locked dependency versions.

## 3. What the Service Does

The service exposes one HTTP endpoint:

- `GET /api/v1/product?upc=<UPC_CODE>`

Its purpose is to:

1. Accept a UPC code from the client.
2. Query a UPC database API to identify the product.
3. Extract the product brand from the UPC result.
4. Query the USPTO API for patent/application data related to that brand.
5. Send the combined product data and patent data to Gemini.
6. Ask Gemini to produce a normalized JSON analysis.
7. Return the synthesized result to the client.

It is an orchestration and enrichment API.

## 4. Request Flow

### 4.1 Entry Point

The server starts in `cmd/api/main.go`.

Responsibilities:

- load local environment variables with `godotenv`
- create a Gin router
- register versioned routes under `/api/v1`
- expose `GET /product`
- run the server on port `8080`

### 4.2 HTTP Handler Flow

The request enters `internal/handlers/product_handler.go`.

Handler responsibilities:

1. Read the `upc` query parameter.
2. Reject the request with `400 Bad Request` if `upc` is missing.
3. Call `services.AggregateProductData(upcCode)`.
4. Return `500 Internal Server Error` if aggregation fails.
5. Return a success payload if aggregation succeeds.

Expected API response envelope:

```json
{
  "status": "success",
  "data": {
    "...": "normalized product insight response"
  }
}
```

### 4.3 Service Orchestration Flow

The main pipeline lives in `internal/services/aggregator.go`.

`AggregateProductData(upcCode string)` performs the following steps:

1. Create a request context with a `45s` timeout.
2. Call `fetchSeedData(ctx, upcCode)`.
3. Validate that a product was returned.
4. Extract the product brand from the first UPC item.
5. Launch patent lookup logic in a goroutine.
6. Wait for the patent lookup to finish.
7. Call `synthesizeWithLLM(ctx, seedData, patentData)`.
8. Ensure the original UPC is set in the final response.
9. Return the normalized result.

Even though the patent request currently runs in a single goroutine and is immediately awaited, the structure suggests the service is being shaped for future fan-out or parallel expansion.

## 5. Detailed Internal Flow

### 5.1 UPC Lookup

Function: `fetchSeedData(ctx, upc string)`

What it does:

- builds a GET request to `https://api.upcitemdb.com/prod/trial/lookup?upc=<code>`
- sends the request with a shared HTTP client
- rejects non-200 responses
- decodes the response into `models.UPCResponse`

Why it matters:

- this is the source of product identity
- brand extracted from this response drives the patent lookup step
- description and features from this response help Gemini produce a richer final summary

### 5.2 USPTO Patent Lookup

Function: `fetchPatentData(ctx, brand string)`

What it does:

- loads `USPTO_API_KEY` from the environment
- constructs a search query based on the product brand:
  `assignee:"<brand>"`
- calls the USPTO API
- attaches required headers:
  - `Accept: application/json`
  - `X-API-Key: <USPTO_API_KEY>`
- rejects non-200 responses and includes the error body
- decodes the result into `models.PatentsViewResponse`

Why it matters:

- this step tries to connect the brand to potentially relevant patent/application records
- the output provides raw patent metadata that Gemini later interprets

### 5.3 LLM Synthesis

Function: `synthesizeWithLLM(ctx, seedData, patentData)`

What it does:

1. Reads and sanitizes `GEMINI_API_KEY`.
2. Marshals UPC and patent data into JSON strings.
3. Builds a strict prompt that:
   - frames the model as a technical and IP analysis assistant
   - asks for JSON only
   - defines the exact response schema
4. Sends the request to:
   `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent`
5. Forces the response MIME type to JSON.
6. Handles non-200 responses with detailed error output.
7. Decodes Gemini's response envelope.
8. Extracts the returned JSON text.
9. Removes optional markdown fences if the model still returns them.
10. Unmarshals the final JSON into `models.FinalProductInfo`.

Why it matters:

- this is the step that converts multiple raw upstream payloads into a single normalized, client-ready response
- instead of hand-writing complex field mapping logic, the design uses an LLM to synthesize structured meaning from heterogeneous inputs

## 6. Output Schema

The final normalized output is defined in `internal/models/product.go`.

Top-level fields:

- `product_id`
- `product_identity`
- `product_description`
- `product_features`
- `core_invention_idea`
- `technical_specifications`
- `intellectual_property_analysis`

### 6.1 Product Identity

Contains:

- `title`
- `brand`
- `model`
- `upc`

### 6.2 Intellectual Property Analysis

Contains:

- `associated_patents`
- `claim_charts`

Each `claim_chart` includes:

- `patent_claim`
- `product_feature_mapped`
- `invalidity_search_relevance`


## 7. Technology Choices and Why They Make Sense
### 7.1 External APIs

#### UPCitemdb: provides concrete product metadata

- resolves UPC codes into product metadata
- provides a starting point for product identity and features

#### USPTO API: provides legal and technical patent-related context

- provides a patent/application data source linked to the brand
- enables IP-oriented enrichment beyond standard product metadata

### 7.2 Gemini: converts raw external data into a structured final insight object

- combines raw product and patent data into structured analysis
- reduces the amount of hard-coded business mapping logic
- can infer relationships between product features and patent concepts


## 8. Current Limitations and Practical Considerations

Based on the current codebase, these are important operational notes:

- the service currently assumes the first UPC item is the correct product match
- patent lookup is based on brand-level search, which may be broad or noisy
- the LLM output is only as reliable as the upstream data and prompt quality
- the patent fetch goroutine does not yet add much concurrency value because it is awaited immediately
- there is no test suite yet
- there is no caching layer, so repeated requests will hit external APIs every time
- there is no retry strategy for transient upstream failures
- there is a debug print in `fetchSeedData`:
  `fmt.Println("data", data)`

## 9. Configuration and Runtime Requirements

Required environment variables:

- `GEMINI_API_KEY`
- `USPTO_API_KEY`

Local development behavior:

- the app attempts to load `.env`
- if `.env` is missing, it falls back to system environment variables

Security note:

- API keys should remain private
- `.env` should not be committed to shared version control
- report documents should never include live secret values

## 10. Example End-to-End Flow

Here is the practical request path:

1. Client sends:
   `GET /api/v1/product?upc=0885909950805`
2. Handler validates the query parameter.
3. Service fetches product metadata from UPCitemdb.
4. Service extracts the product brand.
5. Service queries the USPTO API with the brand.
6. Service sends both raw datasets to Gemini.
7. Gemini returns JSON text describing the normalized result.
8. Service parses that JSON into `FinalProductInfo`.
9. Handler returns:
   - status: `success`
   - data: normalized result

## 11. Suggested Future Enhancements

If this service grows, these would be natural next improvements:

- add unit and integration tests
- add retries with backoff for external API calls
- add structured logging
- add request IDs for traceability
- add response caching by UPC
- validate and sanitize more of the LLM response
- improve patent filtering and ranking before sending to Gemini
- move prompt text into a separate prompt template or config layer
- add OpenAPI or Swagger documentation

## 12. API Response Paste Area

Use the sections below to paste real responses during testing or documentation.

### 12.1 Request Used

```text
GET /api/v1/product?upc=804595002001
```


### 12.2 Final API Response Returned by This Service

```json
{
    "data": {
        "product_id": "NM9170BK_PROD_001",
        "product_identity": {
            "title": "NOVAMEDIC NM-9170-BK Professional Aneroid Sphygmomanometer Blood Pressure Machine and Stethoscope Set, Universal Adult Size Cuff Arm, Manual Emergency BP Monitor Kit with Carrying Case, Black (B09MDJ2QNG)",
            "brand": "Novamedic",
            "model": "NM-9170-BK",
            "upc": "804595002001"
        },
        "product_description": "The NOVAMEDIC NM-9170-BK is a professional aneroid sphygmomanometer blood pressure machine and stethoscope set designed for manual emergency blood pressure monitoring. It features a universal adult size cuff arm and comes with a convenient carrying case. The kit is presented in black, offering a complete solution for healthcare professionals or personal use.",
        "product_features": [
            "Professional Aneroid Sphygmomanometer",
            "Blood Pressure Machine and Stethoscope Set",
            "Universal Adult Size Cuff Arm",
            "Manual Emergency BP Monitor Kit",
            "Includes Carrying Case",
            "Black color"
        ],
        "core_invention_idea": "A comprehensive, manually operated, professional-grade aneroid sphygmomanometer and stethoscope set for accurate blood pressure measurement, packaged for portability and universal adult use.",
        "technical_specifications": {
            "brand": "Novamedic",
            "color": "Black",
            "cuff_size": "Universal Adult Size",
            "included_components": "Sphygmomanometer, Stethoscope, Cuff, Carrying Case",
            "measurement_method": "Aneroid",
            "model_number": "NM-9170-BK",
            "operation_mode": "Manual"
        },
        "intellectual_property_analysis": {
            "associated_patents": [],
            "claim_charts": []
        }
    },
    "status": "success"
}
```


## 13. Short Summary

This service is an AI-assisted enrichment API built in Go. It takes a UPC code, gathers product and patent-related data from external systems, and uses Gemini to transform those inputs into a normalized analysis object. The architecture is simple, practical, and easy to extend, with a strong division between HTTP routing, orchestration logic, and data models.
