# SingleLeaf

A Go-based search aggregation service that routes queries through a rotating Tor proxy pool to SearXNG, returning deduplicated JSON results from multiple search engines across multiple anonymized connections.

![Architecture Diagram](diagram.jpg)

## Architecture

```
Client --> SingleLeaf (:8081) --[5x parallel]--> SearXNG (:8080) --[via Tor Proxy :3128]--> Search Engines
```

Each search query triggers 5 parallel requests to SearXNG. Each request is routed through a different Tor exit IP by the proxy pool's round-robin mechanism. Results are merged, deduplicated by URL, and ranked by accumulated score.

## Stack

| Service | Image | Port | Role |
|---|---|---|---|
| **single-leaf** | Built from source (Go) | 8081 | Query fan-out, deduplication, API |
| **searxng** | `ronxldwilson/searxng-slim` | 8080 | Metasearch engine (JSON API) |
| **tor-proxy** | `ronxldwilson/tor-proxy-pool` | 3128, 4444 | 100 rotating Tor circuits |
| **valkey** | `valkey/valkey:9-alpine` | 6379 | SearXNG cache/session store |

## How It Works

1. **Client** sends `GET /search?q=your+query` to SingleLeaf
2. **SingleLeaf** fires 5 parallel requests to SearXNG (configurable via `SINGLE_LEAF_FANOUT`)
3. **SearXNG** fans out to multiple search engines (Google, Brave, DuckDuckGo, Wikipedia, etc.)
4. Each outbound request from SearXNG is routed through the **Tor proxy pool**, which round-robins across 100 virtual Tor circuits via SOCKS5 auth isolation — every request exits from a different IP
5. **SingleLeaf** collects all 5 responses, deduplicates results by normalized URL, accumulates scores, merges engine lists, and returns a single JSON response sorted by relevance

## Deduplication

- URLs are normalized (strip `www.`, trailing `/`, protocol, lowercase) before comparison
- Duplicate results have their scores summed and engine lists merged
- The longer content snippet is kept
- Answers, suggestions, and infoboxes are also deduplicated

## API

### `GET /search`

| Parameter | Required | Description |
|---|---|---|
| `q` | Yes | Search query |
| `categories` | No | Engine categories (e.g., `general`, `it`, `images`) |
| `lang` | No | Language code (e.g., `en`, `fr`) |
| `pageno` | No | Page number for pagination |

### `GET /health`

Returns `{"status": "ok"}`.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `SINGLE_LEAF_PORT` | `8081` | Listen port |
| `SEARXNG_URL` | `http://searxng:8080` | SearXNG instance URL |
| `SINGLE_LEAF_FANOUT` | `5` | Number of parallel requests per query |
| `TOR_INSTANCES` | `100` | Number of Tor circuits in the proxy pool |
| `TOR_REBUILD_INTERVAL` | `1800` | Tor circuit rebuild interval (seconds) |

## SearXNG Customization

The mounted `searxng/settings.yml` includes:

- **JSON format enabled** alongside HTML
- **Outbound proxy** set to `http://tor-proxy:3128` so all search engine requests go through Tor
- **Engine auto-suspension disabled** — all `suspended_times` set to 0, since each request comes from a fresh Tor exit IP and engines cannot correlate requests

## Quick Start

```bash
docker compose up --build -d
```

Test a search:

```bash
curl "http://localhost:8081/search?q=hello+world"
```

Check Tor proxy stats:

```bash
curl "http://localhost:4444"
```

## Verify IP Rotation

```bash
# Each request should show a different exit IP
for i in $(seq 1 5); do curl -sx localhost:3128 https://httpbin.org/ip; echo; done
```
