# Related Patents — Additional Source Strategy

This document compares candidate patent data sources beyond the **baseline** (USPTO API + SerpAPI Google Patents). It records **heuristic** estimates of **marginal unique recall**—extra relevant IDs a source would add on top of what USPTO + SerpAPI already return—plus rollout order, a pilot method to validate those numbers, and integration notes for this Go service.

## Source comparison matrix

| Dimension | WIPO PATENTSCOPE | Lens.org | EPO Open Patent Services (OPS) | Google `patents-public-data` (BigQuery) |
| --------- | ---------------- | -------- | ------------------------------ | ---------------------------------------- |
| **Primary coverage** | PCT and national filings via WIPO; strong international bibliographic footprint | Global patent/scholarly graph; aggregates multiple authorities | European Patent Office (EP) and related procedural/bibliographic data | Google-hosted public dataset snapshots; bulk bibliographic + claims text in tables |
| **Access model** | REST/SOAP APIs; open search with fair-use expectations | Public API with registration / rate limits; some features tiered | Registered access; weekly/monthly quotas per app key | BigQuery project + billing; SQL over tables |
| **Search style** | Fielded and full-text search over WIPO holdings | Semantic + structured search, citations, aggregates | OPS bibliographic/search endpoints; family/legal status via linked services | Predominantly **analytical**: batch SQL, not an interactive “type query, get ranked list” API per request |
| **Latency fit for online API** | Moderate; suitable with timeouts and caching | Moderate | Moderate; quota-sensitive | Poor for synchronous user request unless precomputed or heavily cached |
| **Licensing / ToS** | WIPO terms; attribution and usage rules | Lens terms of use; API key compliance | EPO legal framework for OPS | Google Cloud + dataset terms |
| **ID / linking** | WO/PCT and national doc IDs | Lens IDs + external patent numbers | EP publications, application numbers | Publication/application fields in schema; join across tables |
| **Operational burden** | Key/registration, rate limits | Key, rate limits, schema drift | Registration, quota monitoring, XML parsing | GCP project, query cost controls, ETL or materialized views |

## Heuristic marginal unique recall (beyond USPTO + SerpAPI)

These percentages are **planning heuristics**, not measured production metrics. They answer: “If we integrated this source well, how much **additional** distinct relevant recall might we expect **after** USPTO + SerpAPI, for typical invention-text queries?”

| Source | Marginal unique recall (heuristic) |
| ------ | ----------------------------------- |
| WIPO PATENTSCOPE | **18%** |
| Lens.org | **14%** |
| EPO OPS | **12%** |
| Google `patents-public-data` (BigQuery) | **6%** |

**Interpretation:** WIPO and Lens fill **international and cross-authority** gaps SerpAPI/USPTO may miss. EPO OPS adds **EP-specific** depth. BigQuery contributes less as a **live** search layer (better for **offline enrichment**, analytics, or pre-built indexes) hence the lower marginal online recall figure.

## Rollout priority order and rationale

1. **WIPO PATENTSCOPE (18%)** — Highest marginal recall in this plan; complements US-centric and Google-ranked results with authoritative **PCT / international** coverage. Fits the same “per-request search” mental model as the current baseline.
2. **Lens.org (14%)** — Second tranche: broad **multi-jurisdiction** aggregation and scholarly links; good for queries where Lens’s index ranks differently from Google Patents.
3. **EPO OPS (12%)** — Third: strong for **European** filings and procedural metadata; quota and XML handling are non-trivial but bounded.
4. **BigQuery `patents-public-data` (6%)** — Last for **interactive** paths: highest integration cost for low per-request marginal gain unless results are **pre-materialized** (scheduled jobs, feature tables) rather than ad hoc SQL per HTTP request.

Rationale summary: prioritize **online-searchable** APIs by descending marginal recall, and treat BigQuery as a **batch / enrichment** track unless product requirements justify a search index built from it.

## Pilot / evaluation method (validate or revise the percentages)

**Goal:** Replace heuristics with measured **incremental recall@k** (or labeled precision-recall) on a fixed evaluation set.

1. **Build a labeled set (e.g. 50–100 queries)**  
   - Source: real or anonymized `invention_text` samples plus synthetic variants (short, long, domain-specific).  
   - For each query, define **relevance** (e.g. human judge: “is this patent relevant to the described invention?”) or use a **pool** of known-positive IDs from prior art searches.

2. **Baseline measurement**  
   - Run current pipeline (USPTO + SerpAPI) only; record ranked/unranked ID lists up to `k` (match production `limit`, e.g. 10 and 50).

3. **Candidate measurement**  
   - For each new source (or shadow integration), fetch top results for the same query with comparable `k`.  
   - Compute **marginal IDs**: IDs that appear in the candidate but not in the baseline **union** for that query.

4. **Aggregate metrics**  
   - **Marginal recall rate:** fraction of queries where at least one **relevant** ID appears only in the new source.  
   - **Unique contribution rate:** average `|relevant marginal IDs| / |relevant IDs in any source|` per query.  
   - Optionally **precision** of marginal IDs (manual or sampled labeling).

5. **Revise planning weights**  
   - Map observed marginal contribution to a revised priority list; update this document (not the original plan file) with **measured** ranges and confidence (e.g. bootstrap over query subset).

6. **Regression guard**  
   - Add automated checks: latency p95, error rate, and “no worse than baseline” on a frozen golden query file in CI (mocked HTTP where possible).

## Integration implications for this Go service

- **Input to providers:** Noise stripping runs at the **start** of `FindRelatedPatentIDs` (startup-loaded regex over `noise_data.csv`). Any future `PatentProvider` receives the **already-cleaned** invention string passed to `Fetch`; it does not see the raw client text unless you read it elsewhere.
- **Concurrency:** Today `FindRelatedPatentIDs` uses a **single timeout context** and an env-built provider slice (`activePatentProviders`), launching one goroutine per active provider. Additional sources should plug into that same pattern (with a **global deadline** and per-client timeouts) or move to a bounded worker pool if provider count grows substantially.
- **Merge order:** `mergeRelatedPatentIDs` is now **variadic round-robin** across successful providers, using `normalizePatentIDKey` for dedupe. New sources can join the merge without changing the core algorithm.
- **Configuration:** Follow the existing env-var pattern (`USPTO_API_KEY`, `SERP_API_KEY` / `SERPAPI_API_KEY`); add one key per provider and let the active-provider registry decide whether that provider participates.
- **Errors:** The current provider orchestration only returns a hard error when **all configured providers fail**; additional sources should preserve that generic behavior.
- **BigQuery specifically:** Prefer **async jobs** or **scheduled exports** into a store the API reads (Postgres, Redis, object storage index), rather than running BigQuery from the request path; if SQL must run online, use **strict** cost and row limits and a **short** dedicated timeout.
- **Testing:** Table-driven tests for merge order, dedupe, and failure matrix (see `internal/services/patent_search_test.go`); add HTTP mocks per new client.

---

*Percentages (18% / 14% / 12% / 6%) are kept as planned until the pilot above produces empirical replacements.*
