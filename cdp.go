package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Crawl4goClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewCrawl4goClient(baseURL string) *Crawl4goClient {
	return &Crawl4goClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type crawlRequest struct {
	URL    string `json:"url"`
	WaitMs int    `json:"wait_ms"`
	Output string `json:"output"`
	Prune  bool   `json:"prune"`
}

type crawlResponse struct {
	URL          string `json:"url"`
	StatusCode   int    `json:"status_code"`
	Blocked      bool   `json:"blocked"`
	BlockReason  string `json:"block_reason,omitempty"`
	Content      string `json:"content"`
	RenderTimeMs int64  `json:"render_time_ms"`
	RenderSource string `json:"render_source"`
}

func (c *Crawl4goClient) CrawlPage(ctx context.Context, pageURL string, waitMs int) (crawlResponse, error) {
	body, err := json.Marshal(crawlRequest{
		URL:    pageURL,
		WaitMs: waitMs,
		Output: "text",
		Prune:  false,
	})
	if err != nil {
		return crawlResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/crawl", bytes.NewReader(body))
	if err != nil {
		return crawlResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return crawlResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 500_000))
	if err != nil {
		return crawlResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return crawlResponse{}, fmt.Errorf("crawl4go returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result crawlResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return crawlResponse{}, err
	}
	return result, nil
}

func (c *Crawl4goClient) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type ExtractedInfo struct {
	Phone   []string `json:"phone,omitempty"`
	Email   []string `json:"email,omitempty"`
	Address string   `json:"address,omitempty"`
	GST     string   `json:"gst,omitempty"`
	About   string   `json:"about,omitempty"`
}

type DeepResult struct {
	URL          string        `json:"url"`
	Title        string        `json:"title"`
	Content      string        `json:"content"`
	PageText     string        `json:"page_text"`
	Extracted    *ExtractedInfo `json:"extracted,omitempty"`
	Engine       string        `json:"engine,omitempty"`
	Engines      []string      `json:"engines,omitempty"`
	Score        float64       `json:"score"`
	Category     string        `json:"category,omitempty"`
	RenderTimeMs int64         `json:"render_time_ms"`
	RenderSource string        `json:"render_source,omitempty"`
	RenderError  string        `json:"render_error,omitempty"`
	Blocked      bool          `json:"blocked,omitempty"`
}

type DeepSearchResponse struct {
	Query           string             `json:"query"`
	TotalResults    int                `json:"total_results"`
	RenderedResults int                `json:"rendered_results"`
	Results         []DeepResult       `json:"results"`
	Synthesis       *SynthesisResponse `json:"synthesis,omitempty"`
	ElapsedMs       int64              `json:"elapsed_ms"`
}

func deepSearchHandler(cfg Config, client *http.Client, crawler *Crawl4goClient, llm *LLMClient, customSearch *CustomSearchClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing query parameter 'q'"})
			return
		}

		renderCount := cfg.DeepRenderCount
		if v := r.URL.Query().Get("render"); v != "" {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
				renderCount = n
			}
		}

		categories := r.URL.Query().Get("categories")
		lang := r.URL.Query().Get("lang")
		pageno := r.URL.Query().Get("pageno")

		params := fmt.Sprintf("q=%s&format=json", url.QueryEscape(query))
		if categories != "" {
			params += "&categories=" + url.QueryEscape(categories)
		}
		if lang != "" {
			params += "&language=" + url.QueryEscape(lang)
		}
		if pageno != "" {
			params += "&pageno=" + url.QueryEscape(pageno)
		}

		searchURL := cfg.SearXNGURL + "/search?" + params
		start := time.Now()

		overallCtx, overallCancel := context.WithTimeout(r.Context(), time.Duration(cfg.DeepTimeoutMs)*time.Millisecond)
		defer overallCancel()

		doSearch := func() (*SearXNGResponse, error) {
			searchCtx, searchCancel := context.WithTimeout(overallCtx, time.Duration(cfg.SearchTimeoutMs)*time.Millisecond)
			defer searchCancel()

			req, err := http.NewRequestWithContext(searchCtx, http.MethodGet, searchURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Accept", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			var parsed SearXNGResponse
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, err
			}
			return &parsed, nil
		}

		var customResults []SearXNGResult
		var searchWg sync.WaitGroup
		var result *SearXNGResponse
		var searchErr error

		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			result, searchErr = doSearch()
			if searchErr == nil && len(result.Results) == 0 {
				searchElapsed := time.Since(start)
				remainingMs := int64(cfg.DeepTimeoutMs) - searchElapsed.Milliseconds()
				if remainingMs > int64(cfg.SearchTimeoutMs) {
					slog.Info("search returned 0 results, retrying", "query", query, "remaining_ms", remainingMs)
					if retry, retryErr := doSearch(); retryErr == nil && len(retry.Results) > 0 {
						result = retry
					}
				}
			}
		}()

		if customSearch.cfg.Enabled {
			searchWg.Add(1)
			go func() {
				defer searchWg.Done()
				cr, err := customSearch.Search(overallCtx, query, 30)
				if err != nil {
					slog.Warn("deep-search custom search failed", "error", err)
					return
				}
				customResults = cr
			}()
		}

		searchWg.Wait()

		if result == nil {
			result = &SearXNGResponse{Query: query, Results: []SearXNGResult{}}
		}

		if len(customResults) > 0 {
			result.Results = append(customResults, result.Results...)
		}

		if len(result.Results) == 0 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "all search requests failed"})
			return
		}

		deepResults := make([]DeepResult, len(result.Results))
		var renderWg sync.WaitGroup
		for i, sr := range result.Results {
			deepResults[i] = DeepResult{
				URL:      sr.URL,
				Title:    sr.Title,
				Content:  sr.Content,
				Engines:  sr.Engines,
				Score:    sr.Score,
				Category: sr.Category,
			}
			if i < renderCount {
				renderWg.Add(1)
				go func(idx int, pageURL string) {
					defer renderWg.Done()
					renderStart := time.Now()

					resp, err := crawler.CrawlPage(overallCtx, pageURL, cfg.DeepWaitMs)
					deepResults[idx].RenderTimeMs = time.Since(renderStart).Milliseconds()

					if err != nil {
						deepResults[idx].RenderError = err.Error()
						return
					}

					text := strings.TrimSpace(resp.Content)
					if len(text) < 50 {
						deepResults[idx].RenderError = "insufficient content"
						return
					}
					if len(text) > 50000 {
						text = text[:50000]
					}

					deepResults[idx].PageText = text
					deepResults[idx].RenderSource = resp.RenderSource
					deepResults[idx].Blocked = resp.Blocked
				}(i, sr.URL)
			}
		}
		renderWg.Wait()

		rendered := 0
		for _, dr := range deepResults {
			if dr.PageText != "" {
				rendered++
			}
		}

		if cfg.LLM.Enabled {
			var extractWg sync.WaitGroup
			for i := range deepResults {
				if deepResults[i].PageText != "" {
					extractWg.Add(1)
					go func(idx int) {
						defer extractWg.Done()
						extractCtx, extractCancel := context.WithTimeout(overallCtx, time.Duration(cfg.LLM.TimeoutMs)*time.Millisecond)
						defer extractCancel()
						_, info, _ := llm.ExtractContent(extractCtx, query, deepResults[idx].PageText)
						if info != nil {
							deepResults[idx].Extracted = info
						}
					}(i)
				}
			}
			extractWg.Wait()
		}

		var synthesis *SynthesisResponse
		if cfg.LLM.Enabled && rendered > 0 {
			synthCtx, synthCancel := context.WithTimeout(overallCtx, time.Duration(cfg.LLM.TimeoutMs)*time.Millisecond)
			s, err := llm.Synthesize(synthCtx, query, deepResults)
			synthCancel()
			if err == nil {
				synthesis = s
			}
		}

		elapsed := time.Since(start)
		slog.Info("deep-search completed",
			"query", query,
			"search_results", len(result.Results),
			"rendered_ok", rendered,
			"rendered_total", min(renderCount, len(result.Results)),
			"synthesized", synthesis != nil,
			"elapsed_ms", elapsed.Milliseconds(),
		)

		writeJSON(w, http.StatusOK, DeepSearchResponse{
			Query:           query,
			TotalResults:    len(result.Results),
			RenderedResults: rendered,
			Results:         deepResults,
			Synthesis:       synthesis,
			ElapsedMs:       elapsed.Milliseconds(),
		})
	}
}
