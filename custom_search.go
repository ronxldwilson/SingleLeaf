package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CustomSearchConfig struct {
	Enabled        bool
	URL            string
	APIKey         string
	APIKeyHeader   string
	QueryParam     string
	LimitParam     string
	ResultsField   string
	URLField       string
	TitleField     string
	ContentField   string
	ScoreField     string
	EngineName      string
	ScoreMultiplier float64
	ExtraParams     string
}

type CustomSearchClient struct {
	cfg        CustomSearchConfig
	httpClient *http.Client
}

func NewCustomSearchClient(cfg CustomSearchConfig) *CustomSearchClient {
	return &CustomSearchClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *CustomSearchClient) Search(ctx context.Context, query string, limit int) ([]SearXNGResult, error) {
	if !c.cfg.Enabled {
		return nil, nil
	}

	params := url.Values{}
	params.Set(c.cfg.QueryParam, query)
	if limit > 0 {
		params.Set(c.cfg.LimitParam, fmt.Sprintf("%d", limit))
	}
	for _, pair := range strings.Split(c.cfg.ExtraParams, "&") {
		if k, v, ok := strings.Cut(pair, "="); ok && k != "" {
			params.Set(k, v)
		}
	}

	reqURL := c.cfg.URL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set(c.cfg.APIKeyHeader, c.cfg.APIKey)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2_000_000))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("custom search returned %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	resultsRaw, ok := raw[c.cfg.ResultsField]
	if !ok {
		return nil, fmt.Errorf("response missing field %q", c.cfg.ResultsField)
	}

	resultsArr, ok := resultsRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("field %q is not an array", c.cfg.ResultsField)
	}

	results := make([]SearXNGResult, 0, len(resultsArr))
	for _, item := range resultsArr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		r := SearXNGResult{
			Engine:  c.cfg.EngineName,
			Engines: []string{c.cfg.EngineName},
		}

		r.URL = getString(m, c.cfg.URLField)
		r.Title = getString(m, c.cfg.TitleField)
		r.Content = getString(m, c.cfg.ContentField)
		r.Score = getFloat(m, c.cfg.ScoreField) * c.cfg.ScoreMultiplier

		if r.URL == "" {
			continue
		}

		results = append(results, r)
	}

	slog.Info("custom search completed",
		"engine", c.cfg.EngineName,
		"query", query,
		"results", len(results),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	return results, nil
}

func getString(m map[string]any, key string) string {
	parts := strings.Split(key, ".")
	current := m
	for i, p := range parts {
		v, ok := current[p]
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
		if next, ok := v.(map[string]any); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func getFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 1.0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 1.0
	}
}
