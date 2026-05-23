package main

import "net/http"

func uiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(uiHTML))
}

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SingleLeaf</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0a0a0a; color: #e0e0e0; min-height: 100vh; }

  .header { text-align: center; padding: 40px 20px 20px; }
  .header img { width: 80px; margin-bottom: 12px; }
  .header h1 { font-size: 28px; font-weight: 600; color: #fff; }
  .header p { color: #888; font-size: 14px; margin-top: 4px; }

  .search-box { max-width: 680px; margin: 20px auto; padding: 0 20px; display: flex; gap: 8px; }
  .search-box input {
    flex: 1; padding: 14px 18px; font-size: 16px; border: 1px solid #333; border-radius: 10px;
    background: #141414; color: #fff; outline: none; transition: border-color 0.2s;
  }
  .search-box input:focus { border-color: #4a9; }
  .search-box button {
    padding: 14px 28px; font-size: 15px; font-weight: 600; border: none; border-radius: 10px;
    background: #2d8a5e; color: #fff; cursor: pointer; transition: background 0.2s;
  }
  .search-box button:hover { background: #36a06e; }
  .search-box button:disabled { background: #333; cursor: not-allowed; }

  .tabs { max-width: 680px; margin: 10px auto; padding: 0 20px; display: flex; gap: 4px; }
  .tab {
    padding: 8px 16px; font-size: 13px; border: 1px solid #333; border-radius: 8px;
    background: transparent; color: #888; cursor: pointer; transition: all 0.2s;
  }
  .tab.active { background: #1a1a1a; color: #4a9; border-color: #4a9; }

  .status { max-width: 680px; margin: 16px auto; padding: 0 20px; }
  .status-bar {
    background: #141414; border: 1px solid #222; border-radius: 10px; padding: 14px 18px;
    font-size: 13px; color: #888; display: none;
  }
  .status-bar.visible { display: block; }
  .status-bar .metric { color: #4a9; font-weight: 600; }
  .status-bar .separator { color: #333; margin: 0 8px; }

  .loader { display: none; text-align: center; padding: 40px; }
  .loader.visible { display: block; }
  .loader .spinner {
    width: 36px; height: 36px; border: 3px solid #222; border-top-color: #4a9;
    border-radius: 50%; animation: spin 0.8s linear infinite; margin: 0 auto 12px;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  .loader p { color: #666; font-size: 14px; }

  .results { max-width: 680px; margin: 0 auto; padding: 0 20px 40px; }

  .result-card {
    background: #141414; border: 1px solid #222; border-radius: 12px;
    padding: 18px; margin-bottom: 12px; transition: border-color 0.2s;
  }
  .result-card:hover { border-color: #333; }
  .result-card .rank { font-size: 11px; color: #555; font-weight: 600; margin-bottom: 6px; }
  .result-card .title { font-size: 16px; font-weight: 600; margin-bottom: 4px; }
  .result-card .title a { color: #5cb88a; text-decoration: none; }
  .result-card .title a:hover { text-decoration: underline; }
  .result-card .url { font-size: 12px; color: #666; margin-bottom: 8px; word-break: break-all; }
  .result-card .snippet { font-size: 14px; color: #aaa; line-height: 1.5; margin-bottom: 10px; }
  .result-card .meta { display: flex; flex-wrap: wrap; gap: 8px; font-size: 11px; }
  .result-card .tag {
    padding: 3px 8px; border-radius: 4px; background: #1a1a1a; color: #888; border: 1px solid #272727;
  }
  .result-card .tag.source-fetch { color: #4a9; border-color: #2a5a3a; }
  .result-card .tag.source-cdp { color: #a94; border-color: #5a4a2a; }
  .result-card .tag.error { color: #c55; border-color: #5a2a2a; }

  .page-text-toggle {
    margin-top: 10px; font-size: 12px; color: #4a9; cursor: pointer;
    background: none; border: none; padding: 0;
  }
  .page-text-toggle:hover { text-decoration: underline; }
  .page-text-content {
    display: none; margin-top: 8px; padding: 12px; background: #0e0e0e;
    border-radius: 8px; font-size: 13px; color: #999; line-height: 1.6;
    max-height: 300px; overflow-y: auto; white-space: pre-wrap; word-break: break-word;
  }
  .page-text-content.visible { display: block; }

  .empty { text-align: center; padding: 60px 20px; color: #555; }
  .empty p { font-size: 15px; }
</style>
</head>
<body>

<div class="header">
  <h1>SingleLeaf</h1>
  <p>Privacy-first search with Tor-routed queries</p>
</div>

<div class="search-box">
  <input type="text" id="query" placeholder="Search anything..." autofocus>
  <button id="searchBtn" onclick="doSearch()">Search</button>
</div>

<div class="tabs">
  <button class="tab active" data-mode="deep-search" onclick="setMode(this)">Deep Search</button>
  <button class="tab" data-mode="search" onclick="setMode(this)">Quick Search</button>
</div>

<div class="status">
  <div class="status-bar" id="statusBar"></div>
</div>

<div class="loader" id="loader">
  <div class="spinner"></div>
  <p id="loaderText">Searching through Tor...</p>
</div>

<div class="results" id="results"></div>

<script>
let mode = 'deep-search';

function setMode(el) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  el.classList.add('active');
  mode = el.dataset.mode;
}

document.getElementById('query').addEventListener('keydown', e => {
  if (e.key === 'Enter') doSearch();
});

async function doSearch() {
  const q = document.getElementById('query').value.trim();
  if (!q) return;

  const btn = document.getElementById('searchBtn');
  const loader = document.getElementById('loader');
  const results = document.getElementById('results');
  const statusBar = document.getElementById('statusBar');
  const loaderText = document.getElementById('loaderText');

  btn.disabled = true;
  loader.classList.add('visible');
  statusBar.classList.remove('visible');
  results.innerHTML = '';
  loaderText.textContent = mode === 'deep-search'
    ? 'Deep searching through Tor + rendering pages...'
    : 'Searching through Tor...';

  const start = performance.now();

  try {
    const url = '/' + mode + '?q=' + encodeURIComponent(q);
    const resp = await fetch(url);
    const data = await resp.json();
    const clientMs = Math.round(performance.now() - start);

    if (data.error) {
      results.innerHTML = '<div class="empty"><p>' + escHtml(data.error) + '</p></div>';
      loader.classList.remove('visible');
      btn.disabled = false;
      return;
    }

    const isDeep = mode === 'deep-search';
    const totalResults = isDeep ? data.total_results : data.number_of_results;
    const items = data.results || [];

    let statusHtml = '<span class="metric">' + totalResults + '</span> results';
    statusHtml += '<span class="separator">|</span>';
    statusHtml += '<span class="metric">' + data.elapsed_ms + 'ms</span> server';
    statusHtml += '<span class="separator">|</span>';
    statusHtml += '<span class="metric">' + clientMs + 'ms</span> total';
    if (isDeep) {
      statusHtml += '<span class="separator">|</span>';
      statusHtml += '<span class="metric">' + data.rendered_results + '/' + items.length + '</span> pages rendered';
    }
    statusBar.innerHTML = statusHtml;
    statusBar.classList.add('visible');

    if (items.length === 0) {
      results.innerHTML = '<div class="empty"><p>No results found. Tor circuits may be slow — try again.</p></div>';
    } else {
      results.innerHTML = items.map((r, i) => renderCard(r, i, isDeep)).join('');
    }
  } catch (err) {
    results.innerHTML = '<div class="empty"><p>Request failed: ' + escHtml(err.message) + '</p></div>';
  }

  loader.classList.remove('visible');
  btn.disabled = false;
}

function renderCard(r, i, isDeep) {
  const hasText = r.page_text && r.page_text.length > 0;
  const snippet = r.content || (hasText ? r.page_text.substring(0, 250) : '');
  const textLen = r.page_text ? r.page_text.length : 0;

  let tags = '';
  if (r.engines && r.engines.length) {
    tags += r.engines.map(e => '<span class="tag">' + escHtml(e) + '</span>').join('');
  }
  if (r.score) {
    tags += '<span class="tag">score: ' + r.score.toFixed(1) + '</span>';
  }
  if (isDeep) {
    if (r.render_source) {
      const cls = r.render_source === 'fetch' ? 'source-fetch' : 'source-cdp';
      tags += '<span class="tag ' + cls + '">' + r.render_source + ' ' + r.render_time_ms + 'ms</span>';
    }
    if (hasText) {
      tags += '<span class="tag source-fetch">' + formatBytes(textLen) + ' extracted</span>';
    }
    if (r.render_error) {
      tags += '<span class="tag error">' + escHtml(r.render_error.substring(0, 40)) + '</span>';
    }
  }

  let pageTextSection = '';
  if (hasText) {
    const id = 'pt-' + i;
    pageTextSection = '<button class="page-text-toggle" onclick="toggleText(\'' + id + '\', this)">Show extracted text (' + formatBytes(textLen) + ')</button>'
      + '<div class="page-text-content" id="' + id + '">' + escHtml(r.page_text.substring(0, 5000)) + (textLen > 5000 ? '\n\n... [truncated, ' + formatBytes(textLen) + ' total]' : '') + '</div>';
  }

  return '<div class="result-card">'
    + '<div class="rank">#' + (i + 1) + '</div>'
    + '<div class="title"><a href="' + escAttr(r.url) + '" target="_blank" rel="noopener">' + escHtml(r.title || r.url) + '</a></div>'
    + '<div class="url">' + escHtml(r.url) + '</div>'
    + (snippet ? '<div class="snippet">' + escHtml(snippet.substring(0, 300)) + '</div>' : '')
    + '<div class="meta">' + tags + '</div>'
    + pageTextSection
    + '</div>';
}

function toggleText(id, btn) {
  const el = document.getElementById(id);
  const visible = el.classList.toggle('visible');
  btn.textContent = visible ? 'Hide extracted text' : btn.textContent.replace('Hide', 'Show');
}

function formatBytes(chars) {
  if (chars < 1000) return chars + ' chars';
  return (chars / 1000).toFixed(1) + 'K chars';
}

function escHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function escAttr(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}
</script>
</body>
</html>`
