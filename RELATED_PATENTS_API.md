# Get Related Patents API

## Overview

This API returns patent identifiers related to a free-text invention description. At **process startup**, the server loads `noise_data.csv` (see path resolution below), builds a **single compiled case-insensitive regex** in memory, and fails fast if loading or compilation fails. On each request, `FindRelatedPatentIDs` **strips known noise phrases** from `invention_text` first, then validates non-emptiness, normalizes `limit`, and fans out to **enabled patent providers** (see provider pattern below). Providers run **in parallel**; results are **round-robin merged** and deduplicated, then capped to the requested limit.

## Endpoint

`POST /api/v1/patents/related`

Registered in `cmd/api/main.go` and handled by `handlers.GetRelatedPatents`.

## Baseline request flow

### Process startup (once)

1. **Noise CSV** — `cmd/api/main.go` resolves the path to `noise_data.csv` (see **Operational notes**), then calls `services.InitializePatentNoiseCleaner`, which reads the CSV once, dedupes phrases, sorts longest-first, and compiles one alternation regex (`(?i)phrase1|phrase2|…`). The HTTP server does not start until this succeeds.

### Per request

1. **HTTP** — Client sends JSON with `invention_text` and optional `limit`.
2. **Handler** (`GetRelatedPatents`) — Binds JSON; returns `400` if the body is invalid JSON or `invention_text` is empty after trim.
3. **Service** — `services.FindRelatedPatentIDs(ctx, inventionText, limit)` using `c.Request.Context()` (inherits Gin’s request context):
   - **Input cleaning** — Applies the startup-loaded regex: matching substrings are replaced with spaces, then whitespace is collapsed and trimmed (`cleanPatentInventionNoise`). This runs **before** the service’s empty-text check and **before** any provider call.
   - **Validation / cap** — If cleaned text is empty → error. Normalizes `limit` (default `10`, max `1000`).
   - **Provider fan-out** — Builds the active provider slice from environment (`activePatentProviders`), then `executePatentProviderSearch` runs each provider’s `Fetch` in its own goroutine with a shared **45s** deadline, collects outcomes, merges successful slices.
4. **Response** — On success, `200` with `status`, `data` echoing the **cleaned** `invention_text`, `limit`, and `patent_ids`.

There is no separate “orchestrator” package: the handler is thin and delegates to `FindRelatedPatentIDs`, which owns cleaning, provider dispatch, and merge.

## Request body

```json
{
  "invention_text": "portable bio-signal measuring device",
  "limit": 10
}
```

### Fields

| Field | Required | Notes |
| ----- | -------- | ----- |
| `invention_text` | Yes | Trimmed by handler; empty after trim → `400`. The service then applies noise stripping; if nothing remains → `500` (“invention text is required”). |
| `limit` | No | Default `10`, max `1000`. Non-positive values are normalized to the default. |

## Response (success)

```json
{
  "status": "success",
  "data": {
    "invention_text": "portable bio-signal measuring device",
    "limit": 10,
    "patent_ids": ["US11234567", "US20230123456"]
  }
}
```

Shape matches `models.RelatedPatentsResponse` (`internal/models/product.go`).

### Output contract

- `patent_ids` is a `[]string`, merged with an **interleaved round-robin** strategy across all successful providers, with **duplicates removed** (comparison uses a normalized key: trim, uppercase, then keep only ASCII letters and digits).
- The slice length is **at most** `limit` (may be shorter if sources return fewer hits).
- Identifiers may include **USPTO application numbers** (`applicationNumberText`), **publication-style IDs** from SerpAPI (`publication_number` preferred, else normalized `patent_id`), **Lens.org document numbers** (`doc_number`), and **EPO OPS publication-style IDs** built as `country + doc-number + kind`. Mixed formats are expected when multiple sources contribute.

## Provider pattern and concurrent sources (USPTO, SerpAPI, Lens.org, EPO OPS)

Each backend is a `services.PatentProvider` (`Name` + `Fetch` function). `activePatentProviders()` builds a **fresh slice per request** from environment: append USPTO when `USPTO_API_KEY` is set, append SerpAPI when a SerpAPI key is set, append Lens.org when `LENS_API_KEY` is set, and append EPO OPS when **both** `EPO_CONSUMER_KEY` and `EPO_CONSUMER_SECRET_KEY` are set. Core orchestration lives in `executePatentProviderSearch` (concurrent `Fetch` calls, outcome collection, then `mergeRelatedPatentIDs`). New providers are added by extending `activePatentProviders()` in the same style.

Configuration is env-driven:

| Variable | Role |
| -------- | ---- |
| `USPTO_API_KEY` | Enables USPTO provider when non-empty (after trim). |
| `SERP_API_KEY` | SerpAPI credential; preferred over `SERPAPI_API_KEY` when both are set (`SerpAPIKey()`). |
| `SERPAPI_API_KEY` | Alternate env name for SerpAPI when `SERP_API_KEY` is unset. |
| `LENS_API_KEY` | Enables Lens.org provider when non-empty (after trim; surrounding quotes are stripped). |
| `EPO_CONSUMER_KEY` | Enables EPO OPS only when paired with `EPO_CONSUMER_SECRET_KEY`. |
| `EPO_CONSUMER_SECRET_KEY` | EPO OPS client secret; both EPO variables must be present to register the provider. |

**Requirement:** At least one provider must be enabled. If the slice is empty, `FindRelatedPatentIDs` returns an error (surfaced as `500` by the handler): `no patent search configured: set USPTO_API_KEY, SERP_API_KEY (or SERPAPI_API_KEY), LENS_API_KEY, and/or EPO_CONSUMER_KEY with EPO_CONSUMER_SECRET_KEY`.

**Execution:**

- A **45s** timeout is applied via `context.WithTimeout` around the whole operation (after cleaning and limit normalization).
- Each active provider runs concurrently in its own goroutine; all outcomes are waited on before merge.

**USPTO path** (`collectUSPTOPatentIDs`):

- Runs several query variants from `buildPatentSearchQueries` (title phrase, quoted full text, keyword `AND` from extracted terms — up to five non-stopword terms ≥3 chars).
- For each query, calls the existing USPTO search helper; collects `applicationNumberText`, dedupes within the USPTO pass, stops when `limit` IDs are collected.
- If every query fails, returns the last error; if some succeed, returns partial IDs.

**SerpAPI path** (`fetchSerpAPIRelatedPatentIDs`):

- Builds several compact Google Patents query variants from extracted patent terms (not raw invention prose).
- `GET` `https://serpapi.com/search.json` with `engine=google_patents`, `q` = current query variant, `num` = `max(20, min(100, limit*3))`, `patents=true`, `scholar=false`.
- Parses `organic_results`; skips scholar rows; prefers `publication_number`, else normalizes `patent_id` (e.g. strips `patent/` prefix). Dedupes and caps to `limit`.
- SerpAPI "no results" responses trigger the next query variant instead of failing the whole SerpAPI provider immediately.
- If the JSON `error` field is present and is **not** recognized as a "no results" case, the SerpAPI provider fails immediately and later query variants are not tried.

**Lens.org path** (`fetchLensOrgRelatedPatentIDs`):

- Builds a keyword-only query from `extractPatentSearchTerms` and joins the extracted terms with spaces (not raw invention prose).
- `POST` `https://api.lens.org/patent/search` with `Authorization: Bearer <key>` and `Content-Type: application/json`.
- Request body includes:
  - `query.query_string.query`
  - `include: ["doc_number"]`
  - `size`
  - `from`
- Uses automatic pagination with a maximum page size of **100**:
  - `size = min(100, limit - len(out))`
  - `from = fetched`
  - stops early when `data` is empty or the page is shorter than requested
- Parses `data[].doc_number`, dedupes with `normalizePatentIDKey`, and stops when `limit` IDs are collected.
- If the **first page** fails (HTTP, read, or decode), the Lens provider returns an error.
- If a **later page** fails after some IDs were already collected, the function logs `Lens.org patent search pagination warning: ...` and returns the partial IDs with **no provider error**.
- If no extractable keywords exist, the Lens provider returns success with no IDs (`nil, nil`).

**EPO OPS path** (`fetchEPORelatedPatentIDs`):

- Builds a keyword-only query from `extractPatentSearchTerms` and joins the extracted terms with spaces (not raw invention prose).
- First obtains an OAuth 2.0 client-credentials token via `getEPOAccessToken`:
  - `POST` `https://ops.epo.org/3.2/auth/accesstoken`
  - body: `grant_type=client_credentials`
  - `Content-Type: application/x-www-form-urlencoded`
  - `Authorization: Basic <base64(consumerKey:consumerSecret)>`
- Then performs published-data search with:
  - `GET` `https://ops.epo.org/3.2/rest-services/published-data/search`
  - query string includes `q` and `Range`
  - headers include `Authorization: Bearer <access_token>`, `Accept: application/json`, and `Range`
- The effective EPO page size is capped to **100** via `epoSearchRangeEnd(limit)`, so the request uses `Range: 1-N` where `N <= 100`.
- Decodes the nested OPS JSON response and reads `ops:publication-reference` entries under `ops:search-result`.
- For each publication reference, prefers a `document-id` whose `@document-id-type` is `docdb`; otherwise uses the first valid `document-id`.
- Builds the outward patent ID as `country + doc-number + kind`, dedupes with `normalizePatentIDKey`, and stops when `limit` IDs are collected.
- If no extractable keywords exist, the EPO provider returns success with no IDs (`nil, nil`).
- Any token, HTTP, read, or decode failure returns an EPO error to the orchestrator, which treats it as a normal partial-provider failure if other sources succeed.

**Merge** (`mergeRelatedPatentIDs`):

- Interleaves IDs from each successful provider in **round-robin** order, dedupes with `normalizePatentIDKey`, and stops at `limit`.

## Error behavior

| Condition | HTTP | Notes |
| --------- | ---- | ----- |
| Invalid JSON body | `400` | `"Invalid JSON request body"`. |
| Missing / empty `invention_text` | `400` | `"Missing 'invention_text' in request body"`. |
| No patent keys configured | `500` | Error text explains `USPTO_API_KEY`, `SERP_API_KEY` (or `SERPAPI_API_KEY`), `LENS_API_KEY`, and/or `EPO_CONSUMER_KEY` with `EPO_CONSUMER_SECRET_KEY`. |
| Single source configured and that source fails | `500` | Upstream / decode / SerpAPI error message. |
| All configured providers fail | `500` | Aggregate message indicates every active provider failed. |
| One or more configured providers fail, but at least one succeeds | `200` | Partial provider failures are **logged** (`related patent search partial failure: provider ... failed: ...`) and successful providers still contribute to the merge, even if some return zero IDs. |
| All enabled providers succeed but return no IDs | `200` | The API returns an empty `patent_ids` array. This can happen when providers extract no usable keywords or upstream searches complete but find nothing. |
| Cleaned `invention_text` is empty | `500` | Possible if the request body had only whitespace/noise phrases removed entirely by the regex. |
| Any other `FindRelatedPatentIDs` error | `500` | Other service failures. |

## Operational notes

- **Patent noise preprocessing:** Before regex-based phrase stripping, the service removes simple HTML-like tags and literal `\n`, `\t`, and `\r` escape spellings from the request text.
- **Patent noise CSV:** Default lookup tries `noise_data.csv` in the process working directory, then `../noise_data.csv`. Override with **`PATENT_NOISE_CSV_PATH`**: non-empty path to an existing regular file (absolute or relative). If set but missing, inaccessible, or a directory, startup fails with an error naming the variable. The CSV’s first column supplies phrases; the header row `Text Collection To Clean` is skipped; rows are deduped case-insensitively.
- **Startup dependency:** The server exits during boot if the noise CSV cannot be read, no phrases load, or regex compilation fails—`cleanPatentInventionNoise` requires a successful `InitializePatentNoiseCleaner`.
- **Recall-first:** Multiple USPTO query shapes plus Google Patents via SerpAPI, Lens.org, and EPO OPS increase recall; `mergeRelatedPatentIDs` interleaves successful providers in **round-robin** order (fair exposure of top ranks), then dedupes.
- **USPTO partial query failures:** the USPTO path tries several query variants; a later USPTO query can fail after an earlier one completed successfully. In that case the USPTO source is treated as a successful source for this request, and any IDs already collected are still eligible for merge.
- **Rate limits / quotas** apply per provider (USPTO API, SerpAPI billing, Lens.org limits, and EPO OPS OAuth/search limits).
