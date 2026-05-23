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
	"sync"
	"sync/atomic"
	"time"

	"regexp"

	"github.com/gorilla/websocket"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var whitespaceRe = regexp.MustCompile(`\s+`)

func httpFetchText(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; SingleLeaf/1.0)")
	req.Header.Set("Accept", "text/html")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 200_000))
	if err != nil {
		return "", err
	}

	html := string(body)

	// Remove script and style blocks
	for _, tag := range []string{"script", "style", "noscript"} {
		re := regexp.MustCompile(`(?is)<` + tag + `[^>]*>.*?</` + tag + `>`)
		html = re.ReplaceAllString(html, " ")
	}

	text := htmlTagRe.ReplaceAllString(html, " ")
	text = whitespaceRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	return text, nil
}

type CDPClient struct {
	zenPandaURL string
	httpClient  *http.Client
}

func NewCDPClient(zenPandaURL string) *CDPClient {
	return &CDPClient{
		zenPandaURL: zenPandaURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

type cdpMessage struct {
	ID        int             `json:"id"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *CDPClient) browserWSURL() string {
	u := strings.TrimPrefix(c.zenPandaURL, "http://")
	u = strings.TrimPrefix(u, "https://")
	return "ws://" + u + "/"
}

func (c *CDPClient) FetchPage(ctx context.Context, targetURL string, waitMs int) (string, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.browserWSURL(), nil)
	if err != nil {
		return "", fmt.Errorf("ws connect: %w", err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	var msgID atomic.Int64

	sendCmd := func(method string, params any, sessionID string) (json.RawMessage, error) {
		id := int(msgID.Add(1))
		p, _ := json.Marshal(params)
		msg := cdpMessage{ID: id, Method: method, Params: p, SessionID: sessionID}
		if err := conn.WriteJSON(msg); err != nil {
			return nil, err
		}
		for {
			var resp cdpMessage
			if err := conn.ReadJSON(&resp); err != nil {
				return nil, err
			}
			if resp.ID == id {
				if resp.Error != nil {
					return nil, fmt.Errorf("cdp error %d: %s", resp.Error.Code, resp.Error.Message)
				}
				return resp.Result, nil
			}
		}
	}

	createResult, err := sendCmd("Target.createTarget", map[string]string{"url": "about:blank"}, "")
	if err != nil {
		return "", fmt.Errorf("create target: %w", err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	json.Unmarshal(createResult, &created)
	targetID := created.TargetID

	attachResult, err := sendCmd("Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, "")
	if err != nil {
		sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")
		return "", fmt.Errorf("attach target: %w", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(attachResult, &attached)
	sid := attached.SessionID

	if _, err := sendCmd("Page.navigate", map[string]string{"url": targetURL}, sid); err != nil {
		sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")
		return "", fmt.Errorf("navigate: %w", err)
	}

	select {
	case <-time.After(time.Duration(waitMs) * time.Millisecond):
	case <-ctx.Done():
		sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")
		return "", ctx.Err()
	}

	result, err := sendCmd("Runtime.evaluate", map[string]any{
		"expression":    "document.body ? document.body.innerText : ''",
		"returnByValue": true,
	}, sid)
	if err != nil {
		sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")
		return "", fmt.Errorf("evaluate: %w", err)
	}

	var evalResult struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &evalResult); err != nil {
		sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")
		return "", err
	}

	sendCmd("Target.closeTarget", map[string]string{"targetId": targetID}, "")

	return evalResult.Result.Value, nil
}

func (c *CDPClient) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.zenPandaURL+"/json/health", nil)
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

type DeepResult struct {
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	PageText     string   `json:"page_text"`
	Engine       string   `json:"engine,omitempty"`
	Engines      []string `json:"engines,omitempty"`
	Score        float64  `json:"score"`
	Category     string   `json:"category,omitempty"`
	RenderTimeMs int64    `json:"render_time_ms"`
	RenderSource string   `json:"render_source,omitempty"`
	RenderError  string   `json:"render_error,omitempty"`
}

type DeepSearchResponse struct {
	Query           string       `json:"query"`
	TotalResults    int          `json:"total_results"`
	RenderedResults int          `json:"rendered_results"`
	Results         []DeepResult `json:"results"`
	ElapsedMs       int64        `json:"elapsed_ms"`
}

func deepSearchHandler(cfg Config, client *http.Client, cdp *CDPClient) http.HandlerFunc {
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

		params := fmt.Sprintf("q=%s&format=json", urlEncode(query))
		if categories != "" {
			params += "&categories=" + urlEncode(categories)
		}
		if lang != "" {
			params += "&language=" + urlEncode(lang)
		}
		if pageno != "" {
			params += "&pageno=" + urlEncode(pageno)
		}

		searchURL := cfg.SearXNGURL + "/search?" + params
		start := time.Now()

		overallCtx, overallCancel := context.WithTimeout(r.Context(), time.Duration(cfg.DeepTimeoutMs)*time.Millisecond)
		defer overallCancel()

		searchDeadline := time.Duration(cfg.SearchTimeoutMs) * time.Millisecond
		searchCtx, searchCancel := context.WithTimeout(overallCtx, searchDeadline)
		defer searchCancel()

		req, err := http.NewRequestWithContext(searchCtx, http.MethodGet, searchURL, nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
			return
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "searxng request failed"})
			return
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to read searxng response"})
			return
		}
		var merged SearXNGResponse
		if err := json.Unmarshal(body, &merged); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid searxng response"})
			return
		}

		toRender := merged.Results
		if len(toRender) > renderCount {
			toRender = toRender[:renderCount]
		}

		deepResults := make([]DeepResult, len(toRender))
		cdpSem := make(chan struct{}, 2)

		var renderWg sync.WaitGroup
		for i, sr := range toRender {
			deepResults[i] = DeepResult{
				URL:      sr.URL,
				Title:    sr.Title,
				Content:  sr.Content,
				Engines:  sr.Engines,
				Score:    sr.Score,
				Category: sr.Category,
			}
			renderWg.Add(1)
			go func(idx int, pageURL string) {
				defer renderWg.Done()
				renderStart := time.Now()

				type fetchResult struct {
					text   string
					source string
				}
				resultCh := make(chan fetchResult, 2)

				// HTTP fetch — fast, plain text extraction
				go func() {
					text, err := httpFetchText(overallCtx, client, pageURL)
					if err != nil || len(strings.TrimSpace(text)) < 200 {
						resultCh <- fetchResult{}
						return
					}
					if len(text) > 50000 {
						text = text[:50000]
					}
					resultCh <- fetchResult{text: text, source: "fetch"}
				}()

				// CDP render — slower but handles JS-rendered pages
				go func() {
					select {
					case cdpSem <- struct{}{}:
						defer func() { <-cdpSem }()
					case <-overallCtx.Done():
						resultCh <- fetchResult{}
						return
					}
					text, err := cdp.FetchPage(overallCtx, pageURL, cfg.DeepWaitMs)
					if err != nil || len(strings.TrimSpace(text)) < 50 {
						resultCh <- fetchResult{}
						return
					}
					if len(text) > 50000 {
						text = text[:50000]
					}
					resultCh <- fetchResult{text: text, source: "cdp"}
				}()

				// Take whichever arrives first with good data
				var best fetchResult
				for received := 0; received < 2; received++ {
					select {
					case r := <-resultCh:
						if r.text != "" && best.text == "" {
							best = r
							// If HTTP fetch won with enough content, don't wait for CDP
							if r.source == "fetch" && len(r.text) >= 500 {
								deepResults[idx].RenderTimeMs = time.Since(renderStart).Milliseconds()
								deepResults[idx].PageText = r.text
								deepResults[idx].RenderSource = r.source
								return
							}
						}
						// If CDP came back with more content, upgrade
						if r.text != "" && r.source == "cdp" && len(r.text) > len(best.text) {
							best = r
						}
					case <-overallCtx.Done():
						deepResults[idx].RenderTimeMs = time.Since(renderStart).Milliseconds()
						if best.text != "" {
							deepResults[idx].PageText = best.text
							deepResults[idx].RenderSource = best.source
						} else {
							deepResults[idx].RenderError = "render deadline exceeded"
						}
						return
					}
				}

				deepResults[idx].RenderTimeMs = time.Since(renderStart).Milliseconds()
				if best.text != "" {
					deepResults[idx].PageText = best.text
					deepResults[idx].RenderSource = best.source
				} else {
					deepResults[idx].RenderError = "no usable content from fetch or render"
				}
			}(i, sr.URL)
		}
		renderWg.Wait()

		rendered := 0
		for _, dr := range deepResults {
			if dr.PageText != "" {
				rendered++
			}
		}

		elapsed := time.Since(start)
		slog.Info("deep-search completed",
			"query", query,
			"search_results", len(merged.Results),
			"rendered_ok", rendered,
			"rendered_total", len(toRender),
			"elapsed_ms", elapsed.Milliseconds(),
		)

		writeJSON(w, http.StatusOK, DeepSearchResponse{
			Query:           query,
			TotalResults:    len(merged.Results),
			RenderedResults: rendered,
			Results:         deepResults,
			ElapsedMs:       elapsed.Milliseconds(),
		})
	}
}

func urlEncode(s string) string {
	return url.QueryEscape(s)
}
