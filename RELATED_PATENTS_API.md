# Get Related Patents API

## Overview

This API returns patent identifiers related to an invention description and/or explicit **search phrases**. At **process startup**, the server loads `noise_data.csv` (see path resolution below), builds a **single compiled case-insensitive regex** in memory, and fails fast if loading or compilation fails.

On each request, `FindRelatedPatentIDs`:

1. **Cleans** `invention_text` with the startup-loaded regex (`cleanPatentInventionNoise`): matching substrings become spaces, whitespace is collapsed and trimmed.
2. **Normalizes** `key_phrases` (trim, drop empties, dedupe case-insensitively).
3. **Fills phrases when missing:** if the client sent no usable `key_phrases` but cleaned `invention_text` is non-empty, the service logs *"No key_phrases provided, using auto-extracted fallback keywords."* and sets phrases from **`extractFallbackKeywords`** (stopword filtering, word-frequency ranking, up to **7** terms). This keeps remote queries **short** and reduces **45s** timeouts from sending long prose.
4. **Validates** that there is at least one usable path: non-empty phrase list after the above, or (with phrases only from user/fallback) non-empty cleaned text where needed for provider-specific fallbacks (see per-provider notes).
5. Normalizes **`limit`**, fans out to **enabled patent providers** in parallel, **round-robin merges** successful results, dedupes, and caps to `limit`.

**Priority:** User-supplied **`key_phrases`** drive query construction for all providers. **`invention_text`** is secondary: it powers fallback keyword extraction when phrases are omitted, and (where documented) bounded full-text snippets—not unbounded raw blobs.

## Endpoint

`POST /api/v1/patents/related`

Registered in `cmd/api/main.go` and handled by `handlers.GetRelatedPatents`.

## Baseline request flow

### Process startup (once)

1. **Noise CSV** — `cmd/api/main.go` resolves the path to `noise_data.csv` (see **Operational notes**), then calls `services.InitializePatentNoiseCleaner`, which reads the CSV once, dedupes phrases, sorts longest-first, and compiles one alternation regex (`(?i)phrase1|phrase2|…`). The HTTP server does not start until this succeeds.

### Per request

1. **HTTP** — Client sends JSON with optional `invention_text`, optional `key_phrases`, and optional `limit`.
2. **Handler** (`GetRelatedPatents`) — Binds JSON; trims `invention_text`; trims each `key_phrases` entry and drops empty strings. Returns **`400`** if **both** trimmed `invention_text` and the cleaned phrase list are empty: `"Provide non-empty 'invention_text' and/or 'key_phrases'"`.
3. **Service** — `services.FindRelatedPatentIDs(ctx, inventionText, keyPhrases, limit)` using `c.Request.Context()` (inherits Gin’s request context):
   - **Input cleaning** — Applies the startup-loaded regex to `invention_text` as above **before** provider calls.
   - **Phrase list** — `normalizeKeyPhrases`; if still empty and cleaned `invention_text` ≠ `""`, **fallback keywords** + log line (see Overview).
   - **Validation** — If there are still no phrases **and** cleaned `invention_text` is empty → error (surfaced as **`500`**: no usable search terms).
   - **Limit** — Default `10`, max `1000`; non-positive values become the default.
   - **Provider fan-out** — `activePatentProviders()` builds the slice; `executePatentProviderSearch` runs each provider’s **`Fetch(ctx, inventionText, keyPhrases, limit)`** in its own goroutine with a shared **45s** deadline, collects outcomes, merges successful slices.
4. **Response** — On success, `200` with `status`, `data` echoing the **cleaned** `invention_text` (may be empty if the client sent only `key_phrases`), `limit`, and `patent_ids`.

There is no separate “orchestrator” package: the handler is thin and delegates to `FindRelatedPatentIDs`, which owns cleaning, phrase resolution, provider dispatch, and merge.

## Request body

### Example: phrases only (recommended for precision)

```json
{
  "key_phrases": ["wearable", "ECG sensor", "noise cancellation"],
  "limit": 10
}
```

### Example: invention text with optional phrases

```json
{
  "invention_text": "portable bio-signal measuring device with adaptive filtering",
  "key_phrases": ["bio-signal", "adaptive filter"],
  "limit": 10
}
```

### Example: invention text only (fallback extraction)

```json
{
  "invention_text": "portable bio-signal measuring device",
  "limit": 10
}
```

### Fields

| Field | Required | Notes |
| ----- | -------- | ----- |
| `invention_text` | No* | Trimmed by handler. Used for noise cleaning and, when `key_phrases` is empty, for **`extractFallbackKeywords`**. Also used for **bounded** supplemental queries (e.g. USPTO title/snippet, SerpAPI/Lens fallbacks) per provider. |
| `key_phrases` | No* | Array of strings; each element trimmed; empty strings removed. **Preferred** input for search accuracy. Duplicates (case-insensitive) removed in the service. |
| `limit` | No | Default `10`, max `1000`. Non-positive values are normalized to the default. |

\* **At least one** of `invention_text` or `key_phrases` must be non-empty after handler trimming; otherwise **`400`**.

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

Shape matches `RelatedPatentsResponse` in `internal/models/product.go`.

### Output contract

- `invention_text` in `data` is the **cleaned** invention string (same pipeline as the service). It may be **`""`** when the client omitted `invention_text` and supplied only `key_phrases`.
- `patent_ids` is a `[]string`, merged with an **interleaved round-robin** strategy across all successful providers, with **duplicates removed** (comparison uses a normalized key: trim, uppercase, then keep only ASCII letters and digits).
- The slice length is **at most** `limit` (may be shorter if sources return fewer hits).
- Identifiers may include **USPTO application numbers** (`applicationNumberText`), **publication-style IDs** from SerpAPI (`publication_number` preferred, else normalized `patent_id`), **Lens.org document numbers** (`doc_number`), and **EPO OPS publication-style IDs** built as `country + doc-number + kind`. Mixed formats are expected when multiple sources contribute.

## Provider pattern and concurrent sources (USPTO, SerpAPI, Lens.org, EPO OPS)

Each backend is a `services.PatentProvider` (`Name` + **`Fetch` function**):

`func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error)`

`activePatentProviders()` builds a **fresh slice per request** from environment: append USPTO when `USPTO_API_KEY` is set, append SerpAPI when a SerpAPI key is set, append Lens.org when `LENS_API_KEY` is set, and append EPO OPS when **both** `EPO_CONSUMER_KEY` and `EPO_CONSUMER_SECRET_KEY` are set. Core orchestration lives in `executePatentProviderSearch` (concurrent `Fetch` calls, outcome collection, then `mergeRelatedPatentIDs`). New providers should use the same `Fetch` signature.

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

- A **45s** timeout is applied via `context.WithTimeout` around the whole operation (after cleaning, phrase resolution, and limit normalization).
- Each active provider runs concurrently in its own goroutine; all outcomes are waited on before merge.

**USPTO path** (`collectUSPTOPatentIDs`):

- Query variants come from **`buildPatentSearchQueries(inventionText, keyPhrases)`** (the old `extractPatentSearchTerms` path has been **removed**).
- **Primary:** `keyPhrases` are turned into **AND** and **OR** segments; multi-word phrases are **quoted** for the USPTO `searchText` parameter.
- **Supplemental (only if cleaned `invention_text` is non-empty):** additional queries use a **truncated** snippet (**200 runes**, not full prose) for `inventionTitle:"…"` and a general quoted phrase, to limit payload size and timeout risk.
- For each query, calls the existing USPTO search helper; collects `applicationNumberText`, dedupes within the USPTO pass, stops when `limit` IDs are collected.
- If every query fails, returns the last error; if some succeed, returns partial IDs.

**SerpAPI path** (`fetchSerpAPIRelatedPatentIDs`):

- **`buildSerpPatentQueries(keyPhrases, inventionText)`** builds compact Google Patents variants: OR-groups, `;`-joined phrases, and smaller OR subsets—**from `keyPhrases`**, not from raw invention prose.
- If **`keyPhrases` is empty** (edge case after normalization/fallback), queries fall back to **cleaned `invention_text` truncated to 400 runes**.
- `GET` `https://serpapi.com/search.json` with `engine=google_patents`, `q` = current query variant, `num` = `max(20, min(100, limit*3))`, `patents=true`, `scholar=false`.
- Parses `organic_results`; skips scholar rows; prefers `publication_number`, else normalizes `patent_id` (e.g. strips `patent/` prefix). Dedupes and caps to `limit`.
- SerpAPI "no results" responses trigger the next query variant instead of failing the whole SerpAPI provider immediately.
- If the JSON `error` field is present and is **not** recognized as a "no results" case, the SerpAPI provider fails immediately and later query variants are not tried.

**Lens.org path** (`fetchLensOrgRelatedPatentIDs`):

- **`query.query_string.query`** is **`strings.Join(keyPhrases, " ")`** (trimmed non-empty phrases).
- If that yields an empty string, falls back to **cleaned `invention_text`** (still bounded by upstream cleaning; prefer sending `key_phrases` for long descriptions).
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
- If both the joined phrases and fallback `invention_text` are empty, the Lens provider returns success with no IDs (`nil, nil`).

**EPO OPS path** (`fetchEPORelatedPatentIDs`):

- Search text **`q`** is **`strings.Join(keyPhrases, " ")`** (trimmed non-empty entries). **`invention_text` is not sent** to EPO search.
- If **`keyPhrases` is empty**, the provider returns success with no IDs (`nil, nil`) immediately—no EPO HTTP call for that request.
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
- Any token, HTTP, read, or decode failure returns an EPO error to the orchestrator, which treats it as a normal partial-provider failure if other sources succeed.

**Merge** (`mergeRelatedPatentIDs`):

- Interleaves IDs from each successful provider in **round-robin** order, dedupes with `normalizePatentIDKey`, and stops at `limit`.

## Error behavior

| Condition | HTTP | Notes |
| --------- | ---- | ----- |
| Invalid JSON body | `400` | `"Invalid JSON request body"`. |
| Both `invention_text` and `key_phrases` empty after trim / drop empties | `400` | `"Provide non-empty 'invention_text' and/or 'key_phrases'"`. |
| No patent keys configured | `500` | Error text explains `USPTO_API_KEY`, `SERP_API_KEY` (or `SERPAPI_API_KEY`), `LENS_API_KEY`, and/or `EPO_CONSUMER_KEY` with `EPO_CONSUMER_SECRET_KEY`. |
| Single source configured and that source fails | `500` | Upstream / decode / SerpAPI error message. |
| All configured providers fail | `500` | Aggregate message indicates every active provider failed. |
| One or more configured providers fail, but at least one succeeds | `200` | Partial provider failures are **logged** (`related patent search partial failure: provider ... failed: ...`) and successful providers still contribute to the merge, even if some return zero IDs. |
| All enabled providers succeed but return no IDs | `200` | The API returns an empty `patent_ids` array. |
| No usable search terms after cleaning and phrase resolution | `500` | e.g. substantive content removed entirely by noise regex and no `key_phrases`. Message indicates providing `key_phrases` and/or substantive `invention_text`. |
| Any other `FindRelatedPatentIDs` error | `500` | Other service failures. |

## Operational notes

- **Patent noise preprocessing:** Before regex-based phrase stripping, the service removes simple HTML-like tags and literal `\n`, `\t`, and `\r` escape spellings from the request text.
- **Patent noise CSV:** Default lookup tries `noise_data.csv` in the process working directory, then `../noise_data.csv`. Override with **`PATENT_NOISE_CSV_PATH`**: non-empty path to an existing regular file (absolute or relative). If set but missing, inaccessible, or a directory, startup fails with an error naming the variable. The CSV’s first column supplies phrases; the header row `Text Collection To Clean` is skipped; rows are deduped case-insensitively.
- **Startup dependency:** The server exits during boot if the noise CSV cannot be read, no phrases load, or regex compilation fails—`cleanPatentInventionNoise` requires a successful `InitializePatentNoiseCleaner`.
- **Recall-first:** Multiple USPTO query shapes plus Google Patents via SerpAPI, Lens.org, and EPO OPS increase recall; `mergeRelatedPatentIDs` interleaves successful providers in **round-robin** order (fair exposure of top ranks), then dedupes.
- **Timeout-friendly queries:** Prefer **`key_phrases`**; long `invention_text` is not sent wholesale to external APIs—USPTO/SerpAPI use **truncated** snippets when full text is used, and the default path uses **short phrase lists** (user or `extractFallbackKeywords`).
- **USPTO partial query failures:** the USPTO path tries several query variants; a later USPTO query can fail after an earlier one completed successfully. In that case the USPTO source is treated as a successful source for this request, and any IDs already collected are still eligible for merge.
- **Rate limits / quotas** apply per provider (USPTO API, SerpAPI billing, Lens.org limits, and EPO OPS OAuth/search limits).
