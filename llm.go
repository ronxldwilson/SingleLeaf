package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type LLMConfig struct {
	Enabled         bool
	BaseURL         string
	APIKey          string
	Model           string
	TimeoutMs       int
	RewritePrompt   string
	RerankPrompt    string
	ExtractPrompt   string
	SynthesizePrompt string
}

type LLMClient struct {
	cfg        LLMConfig
	httpClient *http.Client
}

func NewLLMClient(cfg LLMConfig) *LLMClient {
	return &LLMClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
		},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *LLMClient) complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.1,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 100_000))
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm returned %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

const defaultRewritePrompt = `You are a search query optimizer. Given a user's search query, rewrite it into a more effective search engine query that will return better, more relevant results.

Rules:
- Output ONLY the rewritten query, nothing else
- Keep it concise (under 200 characters)
- Add specific terms that narrow results to the user's likely intent
- Remove ambiguous words, add precise alternatives
- Do NOT add quotes unless they help phrase matching
- Do NOT explain your reasoning`

func (c *LLMClient) RewriteQuery(ctx context.Context, query string) (string, error) {
	if !c.cfg.Enabled {
		return query, nil
	}

	prompt := c.cfg.RewritePrompt
	if prompt == "" {
		prompt = defaultRewritePrompt
	}

	start := time.Now()
	rewritten, err := c.complete(ctx, prompt, query, 200)
	if err != nil {
		slog.Warn("llm rewrite failed, using original query", "error", err, "elapsed_ms", time.Since(start).Milliseconds())
		return query, nil
	}

	rewritten = strings.Trim(rewritten, "\"'`")
	if rewritten == "" {
		return query, nil
	}

	slog.Info("llm rewrite", "original", query, "rewritten", rewritten, "elapsed_ms", time.Since(start).Milliseconds())
	return rewritten, nil
}

const defaultRerankPrompt = `You are a search result relevance ranker. Given a user's query and a list of search results, return the indices of the results reordered by relevance to the query.

Rules:
- Output ONLY a JSON array of indices, e.g. [2,0,4,1,3]
- Most relevant first, least relevant last
- Drop completely irrelevant results by omitting their index
- Judge by title, URL, and snippet — prefer primary sources over aggregators, directories, or unrelated sites
- Do NOT explain your reasoning`

type rerankInput struct {
	Index   int    `json:"i"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func (c *LLMClient) RerankResults(ctx context.Context, query string, results []SearXNGResult, topN int) []SearXNGResult {
	if !c.cfg.Enabled || len(results) == 0 {
		return results
	}

	if topN > len(results) {
		topN = len(results)
	}
	if topN > 30 {
		topN = 30
	}

	items := make([]rerankInput, topN)
	for i := 0; i < topN; i++ {
		snippet := results[i].Content
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		items[i] = rerankInput{
			Index:   i,
			Title:   results[i].Title,
			URL:     results[i].URL,
			Snippet: snippet,
		}
	}

	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return results
	}

	prompt := c.cfg.RerankPrompt
	if prompt == "" {
		prompt = defaultRerankPrompt
	}

	userMsg := fmt.Sprintf("Query: %s\n\nResults:\n%s", query, string(itemsJSON))

	start := time.Now()
	response, err := c.complete(ctx, prompt, userMsg, 500)
	if err != nil {
		slog.Warn("llm rerank failed, using original order", "error", err, "elapsed_ms", time.Since(start).Milliseconds())
		return results
	}

	response = extractJSONArray(response)

	var indices []int
	if err := json.Unmarshal([]byte(response), &indices); err != nil {
		slog.Warn("llm rerank parse failed", "response", response, "error", err)
		return results
	}

	seen := make(map[int]bool)
	reranked := make([]SearXNGResult, 0, len(results))
	for _, idx := range indices {
		if idx >= 0 && idx < topN && !seen[idx] {
			seen[idx] = true
			reranked = append(reranked, results[idx])
		}
	}
	for i := 0; i < topN; i++ {
		if !seen[i] {
			reranked = append(reranked, results[i])
		}
	}
	reranked = append(reranked, results[topN:]...)

	slog.Info("llm rerank", "query", query, "reranked", len(indices), "total", len(results), "elapsed_ms", time.Since(start).Milliseconds())
	return reranked
}

const defaultExtractPrompt = `You are a content extractor. Given a web page's raw text and the user's search query, extract the relevant information into a clean, structured summary.

Rules:
- Output a concise summary (under 500 words) of the page content relevant to the query
- Include key facts: product names, prices, specifications, contact info, company details if present
- Strip navigation, ads, footers, cookie notices, and other boilerplate
- If the page is not relevant to the query, output "NOT_RELEVANT"
- Do NOT make up information that isn't on the page`

func (c *LLMClient) ExtractContent(ctx context.Context, query, pageText string) (string, error) {
	if !c.cfg.Enabled {
		return pageText, nil
	}

	if len(pageText) > 15000 {
		pageText = pageText[:15000]
	}

	prompt := c.cfg.ExtractPrompt
	if prompt == "" {
		prompt = defaultExtractPrompt
	}

	userMsg := fmt.Sprintf("Query: %s\n\nPage text:\n%s", query, pageText)

	start := time.Now()
	extracted, err := c.complete(ctx, prompt, userMsg, 1000)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		slog.Warn("llm extract failed", "error", err, "elapsed_ms", elapsed)
		return pageText, nil
	}

	slog.Info("llm extract", "query", query, "input_len", len(pageText), "output_len", len(extracted), "elapsed_ms", elapsed)
	return extracted, nil
}

const defaultSynthesizePrompt = `You are a research assistant. Given a user's search query and a set of search results with their content, synthesize a comprehensive answer.

Rules:
- Provide a direct, helpful answer to the query
- Cite sources by including [N] references where N is the result number
- Keep it under 800 words
- If results are insufficient to answer, say so honestly
- Do NOT make up information not present in the results`

type synthesizeInput struct {
	Index   int    `json:"i"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type SynthesisResponse struct {
	Query    string `json:"query"`
	Answer   string `json:"answer"`
	Sources  []synthesizeInput `json:"sources"`
	ModelUsed string `json:"model"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

func (c *LLMClient) Synthesize(ctx context.Context, query string, results []DeepResult) (*SynthesisResponse, error) {
	if !c.cfg.Enabled {
		return nil, fmt.Errorf("llm not enabled")
	}

	items := make([]synthesizeInput, 0, 10)
	for i, r := range results {
		if i >= 10 {
			break
		}
		content := r.PageText
		if content == "" {
			content = r.Content
		}
		if len(content) > 3000 {
			content = content[:3000]
		}
		items = append(items, synthesizeInput{
			Index:   i + 1,
			Title:   r.Title,
			URL:     r.URL,
			Content: content,
		})
	}

	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	prompt := c.cfg.SynthesizePrompt
	if prompt == "" {
		prompt = defaultSynthesizePrompt
	}

	userMsg := fmt.Sprintf("Query: %s\n\nResults:\n%s", query, string(itemsJSON))

	start := time.Now()
	answer, err := c.complete(ctx, prompt, userMsg, 2000)
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(start).Milliseconds()
	slog.Info("llm synthesize", "query", query, "sources", len(items), "elapsed_ms", elapsed)

	return &SynthesisResponse{
		Query:     query,
		Answer:    answer,
		Sources:   items,
		ModelUsed: c.cfg.Model,
		ElapsedMs: elapsed,
	}, nil
}

func extractJSONArray(s string) string {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
