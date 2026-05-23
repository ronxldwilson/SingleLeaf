<p align="center">
  <img src="logo.png" alt="SingleLeaf" width="180">
</p>

<h1 align="center">SingleLeaf</h1>

<p align="center">
  <strong>Privacy-first search aggregator with Tor-routed queries and deep page rendering</strong>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#api">API</a> &bull;
  <a href="#architecture">Architecture</a> &bull;
  <a href="#configuration">Configuration</a>
</p>

---

SingleLeaf is a Go service that fans out search queries through [SearXNG](https://github.com/searxng/searxng) across 40+ search engines, routing every request through a pool of 100 rotating Tor circuits. Results are deduplicated, scored, and optionally deep-rendered via a headless browser to extract full page text — all within a strict 10-second deadline.

## Quick Start

```bash
docker compose up -d
```

```bash
# Search
curl "http://localhost:8081/search?q=hello+world"

# Deep search — returns page text for top results
curl "http://localhost:8081/deep-search?q=hello+world"
```

## Architecture

![Architecture Diagram](diagram.jpg)

```
                                                      ┌──────────────┐
                                                 ┌───>│ Search Engine│
                                                 │    └──────────────┘
Client ──> SingleLeaf (:8081) ──[5x fan-out]──> SearXNG (:8080) ──[Tor :3128]──> Search Engines
                │                                                    │
                │                                                    ▼
                │                                              100 Tor circuits
                │                                             (rotating exit IPs)
                │
                └──[top 10 results]──> ZenPanda (:9222) ──> Render via CDP
```

### Services

| Service | Image | Port | Role |
|---|---|---|---|
| **single-leaf** | `ronxldwilson/single-leaf` | 8081 | Query fan-out, deduplication, deep rendering |
| **zenpanda** | `ronxldwilson/zenpanda` | 9222 | Headless Chromium for page rendering (CDP) |
| **searxng** | `ronxldwilson/searxng-slim` | 8080 | Metasearch engine (40+ engines) |
| **tor-proxy** | `ronxldwilson/tor-proxy-pool` | 3128, 4444 | 100 rotating Tor circuits via SOCKS5 isolation |

### How search works

1. Client sends a query to SingleLeaf
2. SingleLeaf fires **5 parallel requests** to SearXNG (configurable)
3. SearXNG fans out to **40+ engines** — Google, Brave, DuckDuckGo, Bing, StackOverflow, Wikipedia, and more
4. Every outbound request routes through the **Tor proxy pool** — each exits from a different IP
5. SingleLeaf **deduplicates** by normalized URL, accumulates scores, merges engine lists, and returns ranked JSON

### How deep search works

1. A single SearXNG request fetches results (within a 7s search timeout)
2. The **top 10 results** are rendered in parallel through ZenPanda using the Chrome DevTools Protocol
3. Full page text is extracted via `document.body.innerText`
4. Everything completes within a **strict 10-second overall deadline** — partial results are returned if time runs out

### Deduplication logic

- URLs normalized: lowercase, strip `www.`, trailing `/`, protocol prefix
- Duplicate scores are summed, engine lists merged, longest content snippet kept
- Answers, suggestions, and infoboxes are also deduplicated

---

## API

### `GET /search`

Standard search with 5x fan-out and deduplication.

| Parameter | Required | Description |
|---|---|---|
| `q` | Yes | Search query |
| `categories` | No | Engine categories (`general`, `it`, `images`, etc.) |
| `lang` | No | Language code (`en`, `fr`, etc.) |
| `pageno` | No | Page number |

### `GET /deep-search`

Search + headless page rendering within a 10s deadline.

| Parameter | Required | Description |
|---|---|---|
| `q` | Yes | Search query |
| `render` | No | Number of results to render (default: 10) |
| `categories` | No | Engine categories |
| `lang` | No | Language code |
| `pageno` | No | Page number |

**Response includes** `page_text` (rendered content), `render_time_ms`, and `render_error` for each result.

### `GET /health`

Returns `{"status": "ok"}`.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `SINGLE_LEAF_PORT` | `8081` | Listen port |
| `SEARXNG_URL` | `http://searxng:8080` | SearXNG URL |
| `ZENPANDA_URL` | `http://zenpanda:9222` | ZenPanda URL |
| `SINGLE_LEAF_FANOUT` | `5` | Parallel requests per search query |
| `DEEP_RENDER_COUNT` | `10` | Top results to deep-render |
| `DEEP_WAIT_MS` | `2000` | Page render wait time (ms) |
| `DEEP_TIMEOUT_MS` | `10000` | Overall deep-search deadline (ms) |
| `SEARCH_TIMEOUT_MS` | `7000` | Search phase timeout (ms) |
| `TOR_INSTANCES` | `100` | Tor circuits in the proxy pool |
| `TOR_REBUILD_INTERVAL` | `1800` | Circuit rebuild interval (seconds) |

### SearXNG

The mounted `searxng/settings.yml` configures:

- **40+ search engines** across general, IT, news, images, videos, science, and packages
- **JSON API enabled** alongside HTML
- **Outbound proxy** set to `http://tor-proxy:3128`
- **Engine auto-suspension disabled** — every request uses a fresh Tor exit
- **3s request timeout / 4s max** for fast responses through Tor

---

## Logging

Structured JSON via Go's `slog`:

```json
{"time":"...","level":"INFO","msg":"search completed","query":"golang","fanout_ok":5,"fanout_total":5,"results":94,"elapsed_ms":4200}
{"time":"...","level":"INFO","msg":"deep-search completed","query":"golang","search_results":94,"rendered_ok":5,"rendered_total":10,"elapsed_ms":9800}
```

## Testing

```bash
go test -v ./...
```

Covers URL normalization, score-based sorting, result deduplication, engine list merging, and answer/suggestion/infobox dedup.

## Verify Tor rotation

```bash
for i in $(seq 1 5); do curl -sx localhost:3128 https://httpbin.org/ip; echo; done
```

Each request exits from a different IP.
