package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Config struct {
	Port         string
	SearXNGURL   string
	TorProxyURL  string
}

func loadConfig() Config {
	return Config{
		Port:        getEnv("SINGLE_LEAF_PORT", "8081"),
		SearXNGURL:  getEnv("SEARXNG_URL", "http://searxng:8080"),
		TorProxyURL: getEnv("TOR_PROXY_URL", "http://tor-proxy:3128"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := loadConfig()

	proxyURL, err := url.Parse(cfg.TorProxyURL)
	if err != nil {
		log.Fatalf("invalid TOR_PROXY_URL: %v", err)
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 60 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   90 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/search", searchHandler(cfg, client))
	mux.HandleFunc("/health", healthHandler)

	log.Printf("single-leaf listening on :%s", cfg.Port)
	log.Printf("searxng: %s | tor-proxy: %s", cfg.SearXNGURL, cfg.TorProxyURL)

	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func searchHandler(cfg Config, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing query parameter 'q'"})
			return
		}

		categories := r.URL.Query().Get("categories")
		lang := r.URL.Query().Get("lang")
		pageno := r.URL.Query().Get("pageno")

		searxURL, err := url.Parse(cfg.SearXNGURL + "/search")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bad searxng url"})
			return
		}

		params := url.Values{}
		params.Set("q", query)
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
		searxURL.RawQuery = params.Encode()

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, searxURL.String(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build request"})
			return
		}
		req.Header.Set("Accept", "application/json")

		start := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(start)

		if err != nil {
			log.Printf("searxng request failed (%v): %v", elapsed, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "searxng request failed"})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to read response"})
			return
		}

		log.Printf("query=%q status=%d elapsed=%v", query, resp.StatusCode, elapsed)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
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
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	fmt.Println(`
     _             _          _            __
 ___(_)_ __   __ _| | ___    | | ___  __ _ / _|
/ __| | '_ \ / _' | |/ _ \___| |/ _ \/ _' | |_
\__ \ | | | | (_| | |  __/___| |  __/ (_| |  _|
|___/_|_| |_|\__, |_|\___|   |_|\___|\__,_|_|
             |___/
	`)
}
