package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DatTruong-tora/product-insight-api/internal/models"
)

const (
	defaultRelatedPatentLimit = 10
	maxRelatedPatentLimit     = 1000

	patentProviderUSPTO   = "uspto"
	patentProviderSerpAPI = "serpapi"
	patentProviderLens    = "lens"
	patentProviderEPO     = "epo"
)

// EPO OPS OAuth and search endpoints (overridable in tests).
var (
	epoOPSAuthURL   = "https://ops.epo.org/3.2/auth/accesstoken"
	epoOPSSearchURL = "https://ops.epo.org/3.2/rest-services/published-data/search"
)

func usptoAPIKeyConfigured() bool {
	return strings.Trim(strings.TrimSpace(os.Getenv("USPTO_API_KEY")), `"'`) != ""
}

func epoOPSConsumerCredentials() (consumerKey, consumerSecret string, ok bool) {
	consumerKey = strings.Trim(strings.TrimSpace(os.Getenv("EPO_CONSUMER_KEY")), `"'`)
	consumerSecret = strings.Trim(strings.TrimSpace(os.Getenv("EPO_CONSUMER_SECRET_KEY")), `"'`)
	if consumerKey == "" || consumerSecret == "" {
		return "", "", false
	}
	return consumerKey, consumerSecret, true
}

// PatentProvider is a registered patent ID source. Add new backends by appending
// to activePatentProviders(); core orchestration stays in executePatentProviderSearch.
type PatentProvider struct {
	Name  string
	Fetch func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error)
}

// activePatentProviders returns providers enabled by environment variables.
func activePatentProviders() []PatentProvider {
	var out []PatentProvider
	if usptoAPIKeyConfigured() {
		out = append(out, PatentProvider{
			Name: patentProviderUSPTO,
			Fetch: func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error) {
				return collectUSPTOPatentIDs(ctx, inventionText, keyPhrases, limit)
			},
		})
	}
	serpKey := SerpAPIKey()
	if strings.TrimSpace(serpKey) != "" {
		key := serpKey
		out = append(out, PatentProvider{
			Name: patentProviderSerpAPI,
			Fetch: func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error) {
				return fetchSerpAPIRelatedPatentIDs(ctx, key, inventionText, keyPhrases, limit)
			},
		})
	}
	lensKey := strings.Trim(strings.TrimSpace(os.Getenv("LENS_API_KEY")), `"'`)
	if lensKey != "" {
		key := lensKey
		out = append(out, PatentProvider{
			Name: patentProviderLens,
			Fetch: func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error) {
				return fetchLensOrgRelatedPatentIDs(ctx, key, inventionText, keyPhrases, limit)
			},
		})
	}
	if ck, cs, ok := epoOPSConsumerCredentials(); ok {
		consumerKey, consumerSecret := ck, cs
		out = append(out, PatentProvider{
			Name: patentProviderEPO,
			Fetch: func(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error) {
				return fetchEPORelatedPatentIDs(ctx, consumerKey, consumerSecret, keyPhrases, limit)
			},
		})
	}
	return out
}

// FindRelatedPatentIDs resolves related patent identifiers from enabled providers
// (USPTO when USPTO_API_KEY is set; SerpAPI Google Patents when SERP_API_KEY or
// SERPAPI_API_KEY is set; Lens.org when LENS_API_KEY is set; EPO OPS when
// EPO_CONSUMER_KEY and EPO_CONSUMER_SECRET_KEY are set). Providers run
// concurrently; partial failures are logged and ignored if at least one provider succeeds.
// keyPhrases is normalized and, when empty, derived from cleaned inventionText via
// extractFallbackKeywords so remote APIs receive compact queries instead of long prose.
func FindRelatedPatentIDs(ctx context.Context, inventionText string, keyPhrases []string, limit int) (*models.RelatedPatentsResponse, error) {
	inventionText = cleanPatentInventionNoise(inventionText)
	keyPhrases = normalizeKeyPhrases(keyPhrases)

	if len(keyPhrases) == 0 && inventionText != "" {
		log.Printf("No key_phrases provided, using auto-extracted fallback keywords.")
		keyPhrases = extractFallbackKeywords(inventionText)
	}

	if len(keyPhrases) == 0 && inventionText == "" {
		return nil, fmt.Errorf("no usable search terms: provide key_phrases and/or invention_text with substantive content after noise cleaning")
	}

	if limit <= 0 {
		limit = defaultRelatedPatentLimit
	}
	if limit > maxRelatedPatentLimit {
		limit = maxRelatedPatentLimit
	}

	providers := activePatentProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no patent search configured: set USPTO_API_KEY, SERP_API_KEY (or SERPAPI_API_KEY), LENS_API_KEY, and/or EPO_CONSUMER_KEY with EPO_CONSUMER_SECRET_KEY")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	return executePatentProviderSearch(ctx, inventionText, keyPhrases, limit, providers)
}

// executePatentProviderSearch runs all providers concurrently and merges results.
// Exposed to tests in this package via same-name calls with injected providers.
func executePatentProviderSearch(ctx context.Context, inventionText string, keyPhrases []string, limit int, providers []PatentProvider) (*models.RelatedPatentsResponse, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("no patent search configured: set USPTO_API_KEY, SERP_API_KEY (or SERPAPI_API_KEY), LENS_API_KEY, and/or EPO_CONSUMER_KEY with EPO_CONSUMER_SECRET_KEY")
	}

	type outcome struct {
		name string
		ids  []string
		err  error
	}
	outcomes := make([]outcome, len(providers))

	var wg sync.WaitGroup
	for i := range providers {
		i := i
		p := providers[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := p.Fetch(ctx, inventionText, keyPhrases, limit)
			outcomes[i] = outcome{name: p.Name, ids: ids, err: err}
		}()
	}
	wg.Wait()

	var failMsgs []string
	var successSlices [][]string
	for _, o := range outcomes {
		if o.err != nil {
			log.Printf("related patent search partial failure: provider %q failed: %v", o.name, o.err)
			failMsgs = append(failMsgs, fmt.Sprintf("%s: %v", o.name, o.err))
			continue
		}
		successSlices = append(successSlices, o.ids)
	}

	if len(successSlices) == 0 {
		return nil, fmt.Errorf("patent search failed: all %d providers failed (%s)", len(providers), strings.Join(failMsgs, "; "))
	}

	merged := mergeRelatedPatentIDs(limit, successSlices...)

	return &models.RelatedPatentsResponse{
		InventionText: inventionText,
		Limit:         limit,
		PatentIDs:     merged,
	}, nil
}

func collectUSPTOPatentIDs(ctx context.Context, inventionText string, keyPhrases []string, limit int) ([]string, error) {
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)
	var lastErr error
	anySuccess := false

	for _, query := range buildPatentSearchQueries(inventionText, keyPhrases) {
		if len(out) >= limit {
			break
		}
		patentData, err := fetchPatentDataBySearchText(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		anySuccess = true
		for _, patentItem := range patentData.PatentFileWrapperDataBag {
			patentID := strings.TrimSpace(patentItem.ApplicationNumberText)
			if patentID == "" {
				continue
			}
			key := normalizePatentIDKey(patentID)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, patentID)
			if len(out) >= limit {
				return out, nil
			}
		}
	}

	if !anySuccess && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// mergeRelatedPatentIDs interleaves patent IDs from N sources in round-robin order
// (fair exposure of top ranks across providers), then dedupes using normalizePatentIDKey.
func mergeRelatedPatentIDs(limit int, sources ...[]string) []string {
	if limit <= 0 || len(sources) == 0 {
		return nil
	}

	maxRound := 0
	for _, s := range sources {
		if n := len(s); n > maxRound {
			maxRound = n
		}
	}

	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)

	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		key := normalizePatentIDKey(raw)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}

	for round := 0; round < maxRound && len(out) < limit; round++ {
		for si := 0; si < len(sources) && len(out) < limit; si++ {
			if round >= len(sources[si]) {
				continue
			}
			add(sources[si][round])
		}
	}

	return out
}

// normalizePatentIDKey returns a canonical form for merge/dedupe only.
// It uppercases, trims, then keeps ASCII letters and digits so equivalent
// surface forms (spaces, commas, slashes, hyphens) map to one key.
// Outward API responses still use each source’s raw patent_ids string.
func normalizePatentIDKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToUpper(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeKeyPhrases(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}

// patentFallbackStopwords filters low-information tokens during auto keyword extraction.
var patentFallbackStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {}, "by": {},
	"for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "into": {}, "is": {}, "it": {},
	"its": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "their": {}, "this": {},
	"to": {}, "was": {}, "were": {}, "which": {}, "with": {}, "without": {}, "using": {},
}

func extractFallbackKeywords(text string) []string {
	normalized := strings.NewReplacer(
		",", " ", ".", " ", ";", " ", ":", " ", "-", " ", "_", " ", "/", " ",
		"(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ",
	).Replace(strings.ToLower(text))
	words := strings.Fields(normalized)

	counts := make(map[string]int)
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		if _, ok := patentFallbackStopwords[w]; ok {
			continue
		}
		counts[w]++
	}

	type wc struct {
		w string
		n int
	}
	pairs := make([]wc, 0, len(counts))
	for w, n := range counts {
		pairs = append(pairs, wc{w: w, n: n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].w < pairs[j].w
	})

	const maxKW = 7
	n := len(pairs)
	if n > maxKW {
		n = maxKW
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pairs[i].w)
	}
	return out
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max]))
}

func quoteUSPTOPatentPhrase(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, `"`, " ")
	p = strings.Join(strings.Fields(p), " ")
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

func buildPatentSearchQueries(inventionText string, keyPhrases []string) []string {
	queries := make([]string, 0, 5)
	seen := make(map[string]struct{}, 5)

	add := func(query string) {
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		if _, ok := seen[query]; ok {
			return
		}
		seen[query] = struct{}{}
		queries = append(queries, query)
	}

	var andParts []string
	for _, kp := range keyPhrases {
		q := quoteUSPTOPatentPhrase(kp)
		if q != "" {
			andParts = append(andParts, q)
		}
	}
	if len(andParts) > 0 {
		add(strings.Join(andParts, " AND "))
	}
	if len(andParts) > 1 {
		add(strings.Join(andParts, " OR "))
	}

	if inventionText != "" {
		snippet := truncateRunes(inventionText, 200)
		if snippet != "" {
			esc := strings.ReplaceAll(snippet, `"`, " ")
			add(fmt.Sprintf(`inventionTitle:"%s"`, esc))
			add(fmt.Sprintf(`"%s"`, esc))
		}
	}

	return queries
}

// --- EPO OPS (published-data search) ---

type epoTokenResponse struct {
	AccessToken string `json:"access_token"`
}

// epoJSONText decodes EPO JSON text nodes encoded as {"$":"value"}.
type epoJSONText struct {
	Dollar string `json:"$"`
}

func epoJSONTextString(t epoJSONText) string {
	return strings.TrimSpace(t.Dollar)
}

// epoSearchDocumentID is one document-id block under publication-reference (OPS JSON).
type epoSearchDocumentID struct {
	DocumentIDType string      `json:"@document-id-type"`
	Country        epoJSONText `json:"country"`
	DocNumber      epoJSONText `json:"doc-number"`
	Kind           epoJSONText `json:"kind"`
}

func (d *epoSearchDocumentID) UnmarshalJSON(b []byte) error {
	var raw struct {
		DocumentIDType string      `json:"@document-id-type"`
		Country        epoJSONText `json:"country"`
		DocNumber      epoJSONText `json:"doc-number"`
		Kind           epoJSONText `json:"kind"`
		CountryOp      epoJSONText `json:"ops:country"`
		DocNumOp       epoJSONText `json:"ops:doc-number"`
		KindOp         epoJSONText `json:"ops:kind"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	d.DocumentIDType = raw.DocumentIDType
	d.Country = raw.Country
	if epoJSONTextString(d.Country) == "" {
		d.Country = raw.CountryOp
	}
	d.DocNumber = raw.DocNumber
	if epoJSONTextString(d.DocNumber) == "" {
		d.DocNumber = raw.DocNumOp
	}
	d.Kind = raw.Kind
	if epoJSONTextString(d.Kind) == "" {
		d.Kind = raw.KindOp
	}
	return nil
}

// epoDocumentIDList unmarshals a single object or array of document-id nodes.
type epoDocumentIDList []epoSearchDocumentID

func (d *epoDocumentIDList) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []epoSearchDocumentID
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*d = arr
		return nil
	}
	var one epoSearchDocumentID
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*d = []epoSearchDocumentID{one}
	return nil
}

// epoPublicationReference is ops:publication-reference in search-result.
type epoPublicationReference struct {
	DocumentID epoDocumentIDList `json:"ops:document-id"`
}

// epoPublicationRefList unmarshals a single publication-reference or an array.
type epoPublicationRefList []epoPublicationReference

func (p *epoPublicationRefList) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []epoPublicationReference
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*p = arr
		return nil
	}
	var one epoPublicationReference
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*p = []epoPublicationReference{one}
	return nil
}

type epoSearchResultNode struct {
	PublicationReference epoPublicationRefList `json:"ops:publication-reference"`
}

type epoBiblioSearchNode struct {
	SearchResult epoSearchResultNode `json:"ops:search-result"`
}

type epoWorldPatentData struct {
	BiblioSearch epoBiblioSearchNode `json:"ops:biblio-search"`
}

type epoSearchResponseRoot struct {
	WorldPatentData epoWorldPatentData `json:"ops:world-patent-data"`
}

func getEPOAccessToken(ctx context.Context, consumerKey, consumerSecret string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	body := strings.NewReader(form.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, epoOPSAuthURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	creds := base64.StdEncoding.EncodeToString([]byte(consumerKey + ":" + consumerSecret))
	req.Header.Set("Authorization", "Basic "+creds)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("EPO OPS token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var tr epoTokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("decode EPO OPS token response: %w", err)
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return "", fmt.Errorf("EPO OPS token response missing access_token")
	}
	return tr.AccessToken, nil
}

func epoSearchRangeEnd(limit int) int {
	const max = 100
	if limit <= 0 {
		return max
	}
	if limit > max {
		return max
	}
	return limit
}

func patentIDFromEPODocumentID(d epoSearchDocumentID) (string, bool) {
	country := epoJSONTextString(d.Country)
	docNum := epoJSONTextString(d.DocNumber)
	kind := epoJSONTextString(d.Kind)
	if country == "" || docNum == "" {
		return "", false
	}
	return country + docNum + kind, true
}

func collectPatentIDsFromEPOSearchResult(refs epoPublicationRefList, limit int) []string {
	if limit <= 0 {
		limit = 100
	}
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)

	add := func(id string) {
		key := normalizePatentIDKey(id)
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}

	for _, pref := range refs {
		if len(out) >= limit {
			break
		}
		ids := pref.DocumentID
		var chosen string
		for _, did := range ids {
			typ := strings.TrimSpace(did.DocumentIDType)
			id, ok := patentIDFromEPODocumentID(did)
			if !ok {
				continue
			}
			if typ == "docdb" {
				chosen = id
				break
			}
			if chosen == "" {
				chosen = id
			}
		}
		if chosen != "" {
			add(chosen)
		}
	}
	return out
}

func fetchEPORelatedPatentIDs(ctx context.Context, consumerKey, consumerSecret string, keyPhrases []string, limit int) ([]string, error) {
	if len(keyPhrases) == 0 {
		return nil, nil
	}
	var qParts []string
	for _, kp := range keyPhrases {
		kp = strings.TrimSpace(kp)
		if kp != "" {
			qParts = append(qParts, kp)
		}
	}
	q := strings.Join(qParts, " ")
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}

	token, err := getEPOAccessToken(ctx, consumerKey, consumerSecret)
	if err != nil {
		return nil, err
	}

	rangeEnd := epoSearchRangeEnd(limit)
	params := url.Values{}
	params.Set("q", q)
	params.Set("Range", fmt.Sprintf("1-%d", rangeEnd))
	reqURL := epoOPSSearchURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Range", fmt.Sprintf("1-%d", rangeEnd))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EPO OPS search returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var root epoSearchResponseRoot
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode EPO OPS search response: %w", err)
	}
	refs := root.WorldPatentData.BiblioSearch.SearchResult.PublicationReference

	collLimit := limit
	if collLimit <= 0 {
		collLimit = 100
	}

	return collectPatentIDsFromEPOSearchResult(refs, collLimit), nil
}
