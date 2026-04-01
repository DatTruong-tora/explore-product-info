# Get Related Patents API

## Overview

This API returns patent identifiers related to a free-text invention description. The baseline pipeline fans out to **USPTO** (when configured) and **SerpAPI Google Patents** (when configured) **in parallel**, merges and deduplicates results, and caps the list to the requested limit.

## Endpoint

`POST /api/v1/patents/related`

Registered in `cmd/api/main.go` and handled by `handlers.GetRelatedPatents`.

## Baseline request flow

1. **HTTP** — Client sends JSON with `invention_text` and optional `limit`.
2. **Handler** (`GetRelatedPatents`) — Binds JSON; returns `400` if the body is invalid JSON or `invention_text` is empty after trim.
3. **Service** — `services.FindRelatedPatentIDs(ctx, inventionText, limit)` runs the patent resolution logic using `c.Request.Context()` (inherits Gin’s request context).
4. **Response** — On success, `200` with `status`, `data` echoing `invention_text`, `limit`, and `patent_ids`.

There is no separate “orchestrator” package: the handler is thin and delegates entirely to `FindRelatedPatentIDs`.

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
| `invention_text` | Yes | Trimmed; empty after trim → `400`. |
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

- `patent_ids` is a `[]string`, ordered **USPTO results first**, then **SerpAPI** results, with **duplicates removed** (comparison uses a normalized key: trim → uppercase → keep ASCII letters and digits only).
- The slice length is **at most** `limit` (may be shorter if sources return fewer hits).
- Identifiers may be **USPTO application numbers** (`applicationNumberText` from USPTO) and/or **publication-style IDs** from SerpAPI (`publication_number` preferred, else parsed `patent_id`). Mixed formats are expected when both sources contribute.

## Concurrent sources (USPTO + SerpAPI)

Configuration is env-driven:

| Variable | Role |
| -------- | ---- |
| `USPTO_API_KEY` | Enables USPTO search when non-empty (after trim). |
| `SERP_API_KEY` | Enables SerpAPI; `SERP_API_KEY` wins if both are set. |

**Requirement:** At least one of the above must be set. If neither is configured, `FindRelatedPatentIDs` returns an error (surfaced as `500` by the handler).

**Execution:**

- A **45s** timeout is applied via `context.WithTimeout` around the whole operation.
- If USPTO is configured, `collectUSPTOPatentIDs` runs in its own goroutine.
- If SerpAPI is configured, `fetchSerpAPIRelatedPatentIDs` runs in its own goroutine.
- Both complete (`sync.WaitGroup`) before merge.

**USPTO path** (`collectUSPTOPatentIDs`):

- Runs several query variants from `buildPatentSearchQueries` (title phrase, quoted full text, keyword `AND` from extracted terms — up to five non-stopword terms ≥3 chars).
- For each query, calls the existing USPTO search helper; collects `applicationNumberText`, dedupes within the USPTO pass, stops when `limit` IDs are collected.
- If every query fails, returns the last error; if some succeed, returns partial IDs.

**SerpAPI path** (`fetchSerpAPIRelatedPatentIDs`):

- `GET` `https://serpapi.com/search.json` with `engine=google_patents`, `q` = invention text, `num` = `max(20, min(100, limit*3))`, `patents=true`, `scholar=false`.
- Parses `organic_results`; skips scholar rows; prefers `publication_number`, else normalizes `patent_id` (e.g. strips `patent/` prefix). Dedupes and caps to `limit`.

**Merge** (`mergeRelatedPatentIDs`):

- Preserves **USPTO order**, then appends SerpAPI IDs not already seen, until `limit` total.

## Error behavior

| Condition | HTTP | Notes |
| --------- | ---- | ----- |
| Invalid JSON body | `400` | `"Invalid JSON request body"`. |
| Missing / empty `invention_text` | `400` | `"Missing 'invention_text' in request body"`. |
| No patent keys configured | `500` | Error text explains `USPTO_API_KEY` and/or `SERP_API_KEY`. |
| Single source configured and that source fails | `500` | Upstream / decode / SerpAPI error message. |
| **Both** sources configured and **both** fail | `500` | Combined message: USPTO and SerpAPI errors. |
| Both configured; **one** fails | `200` | Partial failure is **logged** (`related patent search partial failure: …`) as long as the other configured source completes without error, even if that source returns zero IDs. |
| Any other `FindRelatedPatentIDs` error | `500` | e.g. empty invention text (defensive; handler already rejects). |

## Operational notes

- **Recall-first:** Multiple USPTO query shapes plus Google Patents via SerpAPI increase recall; merge order prioritizes USPTO for stability of “primary” IDs.
- **USPTO partial query failures:** the USPTO path tries several query variants; a later USPTO query can fail after an earlier one completed successfully. In that case the USPTO source is treated as a successful source for this request, and any IDs already collected are still eligible for merge.
- **Rate limits / quotas** apply per provider (USPTO API and SerpAPI billing).
- For planned additional sources and rollout order, see `RELATED_PATENTS_SOURCE_STRATEGY.md`.
