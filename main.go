package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Port            string
	SearXNGURL      string
	FanOut          int
	Crawl4goURL     string
	DeepRenderCount int
	DeepWaitMs      int
	DeepTimeoutMs   int
	SearchTimeoutMs int
	LLM             LLMConfig
	CustomSearch    CustomSearchConfig
}

func loadConfig() Config {
	fanOut := 5
	if v := os.Getenv("SINGLE_LEAF_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fanOut = n
		}
	}
	renderCount := 10
	if v := os.Getenv("DEEP_RENDER_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			renderCount = n
		}
	}
	waitMs := 2000
	if v := os.Getenv("DEEP_WAIT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			waitMs = n
		}
	}
	deepTimeoutMs := 15000
	if v := os.Getenv("DEEP_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			deepTimeoutMs = n
		}
	}
	searchTimeoutMs := 8000
	if v := os.Getenv("SEARCH_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			searchTimeoutMs = n
		}
	}
	llmTimeoutMs := 10000
	if v := os.Getenv("LLM_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			llmTimeoutMs = n
		}
	}

	apiKey := getEnv("LLM_API_KEY", "")
	llmCfg := LLMConfig{
		Enabled:          apiKey != "",
		BaseURL:          getEnv("LLM_BASE_URL", "https://api.groq.com/openai/v1"),
		APIKey:           apiKey,
		Model:            getEnv("LLM_MODEL", "llama-3.3-70b-versatile"),
		TimeoutMs:        llmTimeoutMs,
		RewritePrompt:    getEnv("LLM_REWRITE_PROMPT", ""),
		RerankPrompt:     getEnv("LLM_RERANK_PROMPT", ""),
		ExtractPrompt:    getEnv("LLM_EXTRACT_PROMPT", ""),
		SynthesizePrompt: getEnv("LLM_SYNTHESIZE_PROMPT", ""),
	}

	scoreMultiplier := 1.0
	if v := os.Getenv("CUSTOM_SEARCH_SCORE_MULTIPLIER"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			scoreMultiplier = f
		}
	}

	customURL := getEnv("CUSTOM_SEARCH_URL", "")
	customCfg := CustomSearchConfig{
		Enabled:         customURL != "",
		URL:             customURL,
		APIKey:          getEnv("CUSTOM_SEARCH_API_KEY", ""),
		APIKeyHeader:    getEnv("CUSTOM_SEARCH_API_KEY_HEADER", "X-API-Key"),
		QueryParam:      getEnv("CUSTOM_SEARCH_QUERY_PARAM", "q"),
		LimitParam:      getEnv("CUSTOM_SEARCH_LIMIT_PARAM", "limit"),
		ResultsField:    getEnv("CUSTOM_SEARCH_RESULTS_FIELD", "results"),
		URLField:        getEnv("CUSTOM_SEARCH_URL_FIELD", "url"),
		TitleField:      getEnv("CUSTOM_SEARCH_TITLE_FIELD", "title"),
		ContentField:    getEnv("CUSTOM_SEARCH_CONTENT_FIELD", "description"),
		ScoreField:      getEnv("CUSTOM_SEARCH_SCORE_FIELD", "score"),
		EngineName:      getEnv("CUSTOM_SEARCH_ENGINE_NAME", "custom"),
		ScoreMultiplier: scoreMultiplier,
		ExtraParams:     getEnv("CUSTOM_SEARCH_EXTRA_PARAMS", ""),
	}

	return Config{
		Port:            getEnv("SINGLE_LEAF_PORT", "8081"),
		SearXNGURL:      getEnv("SEARXNG_URL", "http://searxng:8080"),
		FanOut:          fanOut,
		Crawl4goURL:     getEnv("CRAWL4GO_URL", "http://crawl4go:8082"),
		DeepRenderCount: renderCount,
		DeepWaitMs:      waitMs,
		DeepTimeoutMs:   deepTimeoutMs,
		SearchTimeoutMs: searchTimeoutMs,
		LLM:             llmCfg,
		CustomSearch:    customCfg,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type fanResult struct {
	resp *SearXNGResponse
	err  error
}

type SearXNGResponse struct {
	Query               string           `json:"query"`
	NumberOfResults     int              `json:"number_of_results"`
	Results             []SearXNGResult  `json:"results"`
	Answers             []any            `json:"answers"`
	Corrections         []any            `json:"corrections"`
	Infoboxes           []any            `json:"infoboxes"`
	Suggestions         []any            `json:"suggestions"`
	UnresponsiveEngines []any            `json:"unresponsive_engines"`
}

type SearXNGResult struct {
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Engine        string   `json:"engine"`
	Engines       []string `json:"engines"`
	Score         float64  `json:"score"`
	Category      string   `json:"category"`
	PublishedDate any      `json:"publishedDate"`
	Thumbnail     string   `json:"thumbnail,omitempty"`
	ImgSrc        string   `json:"img_src,omitempty"`
	Template      string   `json:"template,omitempty"`
}

func main() {
	cfg := loadConfig()

	client := &http.Client{
		Timeout: 90 * time.Second,
	}

	crawler := NewCrawl4goClient(cfg.Crawl4goURL)
	llm := NewLLMClient(cfg.LLM)
	customSearch := NewCustomSearchClient(cfg.CustomSearch)

	mux := http.NewServeMux()
	mux.HandleFunc("/", uiHandler)
	mux.HandleFunc("/search", searchHandler(cfg, client, llm, customSearch))
	mux.HandleFunc("/deep-search", deepSearchHandler(cfg, client, crawler, llm, customSearch))
	mux.HandleFunc("/health", healthHandler)

	slog.Info("single-leaf starting",
		"port", cfg.Port,
		"fanout", cfg.FanOut,
		"render_count", cfg.DeepRenderCount,
		"wait_ms", cfg.DeepWaitMs,
		"deep_timeout_ms", cfg.DeepTimeoutMs,
		"llm_enabled", cfg.LLM.Enabled,
		"custom_search", cfg.CustomSearch.Enabled,
	)
	slog.Info("upstream services",
		"searxng", cfg.SearXNGURL,
		"crawl4go", cfg.Crawl4goURL,
		"llm", cfg.LLM.BaseURL,
		"custom_search", cfg.CustomSearch.URL,
	)

	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func searchHandler(cfg Config, client *http.Client, llm *LLMClient, customSearch *CustomSearchClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing query parameter 'q'"})
			return
		}

		categories := r.URL.Query().Get("categories")
		lang := r.URL.Query().Get("lang")
		pageno := r.URL.Query().Get("pageno")

		searchQuery := query
		if cfg.LLM.Enabled {
			rewriteCtx, rewriteCancel := context.WithTimeout(r.Context(), time.Duration(cfg.LLM.TimeoutMs)*time.Millisecond)
			rewritten, _ := llm.RewriteQuery(rewriteCtx, query)
			rewriteCancel()
			if rewritten != query {
				searchQuery = rewritten
			}
		}

		params := url.Values{}
		params.Set("q", searchQuery)
		params.Set("format", "json")
		if categories != "" {
			params.Set("categories", categories)
		}
		if lang != "" {
			params.Set("language", lang)
		}
		if pageno != "" {
			params.Set("pageno", pageno)
		}

		searchURL := cfg.SearXNGURL + "/search?" + params.Encode()

		start := time.Now()
		searchCtx, searchCancel := context.WithTimeout(r.Context(), time.Duration(cfg.SearchTimeoutMs)*time.Millisecond)
		defer searchCancel()

		results := make([]fanResult, cfg.FanOut)
		var customResults []SearXNGResult
		var wg sync.WaitGroup

		for i := range cfg.FanOut {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				req, err := http.NewRequestWithContext(searchCtx, http.MethodGet, searchURL, nil)
				if err != nil {
					results[idx] = fanResult{err: err}
					return
				}
				req.Header.Set("Accept", "application/json")

				resp, err := client.Do(req)
				if err != nil {
					results[idx] = fanResult{err: err}
					return
				}
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				if err != nil {
					results[idx] = fanResult{err: err}
					return
				}

				var parsed SearXNGResponse
				if err := json.Unmarshal(body, &parsed); err != nil {
					results[idx] = fanResult{err: err}
					return
				}
				results[idx] = fanResult{resp: &parsed}
			}(i)
		}

		if customSearch.cfg.Enabled {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cr, err := customSearch.Search(searchCtx, query, 30)
				if err != nil {
					slog.Warn("custom search failed", "error", err)
					return
				}
				customResults = cr
			}()
		}

		wg.Wait()

		merged := mergeResults(results, query)

		if len(customResults) > 0 {
			merged = mergeCustomResults(merged, customResults)
		}

		// Retry once with a single request if all fanout failed
		successCount := 0
		for _, fr := range results {
			if fr.err == nil {
				successCount++
			}
		}
		if len(merged.Results) == 0 && successCount == 0 {
			slog.Info("all fanout failed, retrying with single request", "query", query)
			retryCtx, retryCancel := context.WithTimeout(r.Context(), time.Duration(cfg.SearchTimeoutMs)*time.Millisecond)
			defer retryCancel()
			req, err := http.NewRequestWithContext(retryCtx, http.MethodGet, searchURL, nil)
			if err == nil {
				req.Header.Set("Accept", "application/json")
				resp, err := client.Do(req)
				if err == nil {
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					if err == nil {
						var parsed SearXNGResponse
						if json.Unmarshal(body, &parsed) == nil {
							merged = parsed
							successCount = 1
						}
					}
				}
			}
		}

		elapsed := time.Since(start)
		slog.Info("search completed",
			"query", query,
			"fanout_ok", successCount,
			"fanout_total", cfg.FanOut,
			"results", len(merged.Results),
			"elapsed_ms", elapsed.Milliseconds(),
		)

		if successCount == 0 && len(merged.Results) == 0 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "all search requests failed"})
			return
		}

		if cfg.LLM.Enabled && len(merged.Results) > 0 {
			rerankCtx, rerankCancel := context.WithTimeout(r.Context(), time.Duration(cfg.LLM.TimeoutMs)*time.Millisecond)
			merged.Results = llm.RerankResults(rerankCtx, query, merged.Results, 30)
			rerankCancel()
		}

		merged.Query = query

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(merged)
	}
}

func mergeResults(fanOutResults []fanResult, query string) SearXNGResponse {
	seen := make(map[string]*SearXNGResult)
	var ordered []string
	seenAnswers := make(map[string]bool)
	seenSuggestions := make(map[string]bool)
	seenInfoboxIDs := make(map[string]bool)

	merged := SearXNGResponse{
		Query:   query,
		Results: []SearXNGResult{},
		Answers: []any{},
		Corrections: []any{},
		Infoboxes:   []any{},
		Suggestions: []any{},
		UnresponsiveEngines: []any{},
	}

	for _, fr := range fanOutResults {
		if fr.err != nil || fr.resp == nil {
			continue
		}
		r := fr.resp

		for _, result := range r.Results {
			normalizedURL := normalizeURL(result.URL)
			if existing, ok := seen[normalizedURL]; ok {
				existing.Score += result.Score
				for _, eng := range result.Engines {
					if !containsStr(existing.Engines, eng) {
						existing.Engines = append(existing.Engines, eng)
					}
				}
				if len(result.Content) > len(existing.Content) {
					existing.Content = result.Content
				}
			} else {
				dup := result
				seen[normalizedURL] = &dup
				ordered = append(ordered, normalizedURL)
			}
		}

		for _, a := range r.Answers {
			key := fmt.Sprintf("%v", a)
			if !seenAnswers[key] {
				seenAnswers[key] = true
				merged.Answers = append(merged.Answers, a)
			}
		}

		for _, s := range r.Suggestions {
			key := fmt.Sprintf("%v", s)
			if !seenSuggestions[key] {
				seenSuggestions[key] = true
				merged.Suggestions = append(merged.Suggestions, s)
			}
		}

		for _, ib := range r.Infoboxes {
			if m, ok := ib.(map[string]any); ok {
				id := fmt.Sprintf("%v", m["id"])
				if !seenInfoboxIDs[id] {
					seenInfoboxIDs[id] = true
					merged.Infoboxes = append(merged.Infoboxes, ib)
				}
			} else {
				merged.Infoboxes = append(merged.Infoboxes, ib)
			}
		}

		merged.Corrections = append(merged.Corrections, r.Corrections...)
	}

	for _, u := range ordered {
		merged.Results = append(merged.Results, *seen[u])
	}

	applyDomainScoring(merged.Results)
	sortResultsByScore(merged.Results)

	merged.NumberOfResults = len(merged.Results)
	return merged
}

func mergeCustomResults(merged SearXNGResponse, custom []SearXNGResult) SearXNGResponse {
	seen := make(map[string]bool)
	for _, r := range merged.Results {
		seen[normalizeURL(r.URL)] = true
	}

	for _, r := range custom {
		norm := normalizeURL(r.URL)
		if seen[norm] {
			for i := range merged.Results {
				if normalizeURL(merged.Results[i].URL) == norm {
					merged.Results[i].Score += r.Score
					for _, eng := range r.Engines {
						if !containsStr(merged.Results[i].Engines, eng) {
							merged.Results[i].Engines = append(merged.Results[i].Engines, eng)
						}
					}
					break
				}
			}
		} else {
			seen[norm] = true
			merged.Results = append(merged.Results, r)
		}
	}

	sortResultsByScore(merged.Results)
	merged.NumberOfResults = len(merged.Results)
	return merged
}

var lowPriorityDomains = []string{
	"indiamart.com", "dir.indiamart.com", "tradeindia.com",
	"alibaba.com", "aliexpress.com", "made-in-china.com",
	"globalsources.com", "thomasnet.com", "exportersindia.com",
	"justdial.com", "sulekha.com", "yellowpages.com",
	"dial4trade.com", "go4worldbusiness.com", "zauba.com",
	"kompass.com", "tradewheel.com", "ec21.com", "ecplaza.net",
	"wikipedia.org", "wikisource.org", "wiktionary.org",
	"wikidata.org", "wikibooks.org", "wikiquote.org",
	"cambridge.org", "merriam-webster.com", "dictionary.com",
	"britannica.com", "bab.la", "linguee.com", "glosbe.com",
	"indeed.com", "pinterest.com", "quora.com", "reddit.com",
	"facebook.com", "instagram.com", "twitter.com", "x.com",
	"linkedin.com", "openlibrary.org", "archive.org", "stason.org",
}

func applyDomainScoring(results []SearXNGResult) {
	for i := range results {
		norm := normalizeURL(results[i].URL)
		for _, domain := range lowPriorityDomains {
			if strings.Contains(norm, domain) {
				results[i].Score *= 0.3
				break
			}
		}
	}
}

func sortResultsByScore(results []SearXNGResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
}

func normalizeURL(rawURL string) string {
	rawURL = strings.ToLower(rawURL)
	rawURL = strings.TrimRight(rawURL, "/")
	rawURL = strings.TrimPrefix(rawURL, "https://www.")
	rawURL = strings.TrimPrefix(rawURL, "http://www.")
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	return rawURL
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	fmt.Println(`
     _             _          _            __
 ___(_)_ __   __ _| | ___    | | ___  __ _ / _|
/ __| | '_ \ / _' | |/ _ \___| |/ _ \/ _' | |_
\__ \ | | | | (_| | |  __/___| |  __/ (_| |  _|
|___/_|_| |_|\__, |_|\___|   |_|\___|\__,_|_|
             |___/
	`)
}
