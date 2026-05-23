package main

import (
	"fmt"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/", "example.com"},
		{"http://example.com/", "example.com"},
		{"https://www.example.com/", "example.com"},
		{"http://www.example.com/", "example.com"},
		{"https://example.com/path", "example.com/path"},
		{"https://example.com/path/", "example.com/path"},
		{"HTTPS://EXAMPLE.COM/Path", "example.com/path"},
		{"https://www.example.com///", "example.com"},
		{"https://sub.example.com/page", "sub.example.com/page"},
		{"example.com/no-scheme", "example.com/no-scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeURL(tt.input)
			if got != tt.want {
				t.Errorf("normalizeURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsStr(t *testing.T) {
	tests := []struct {
		slice []string
		s     string
		want  bool
	}{
		{[]string{"a", "b", "c"}, "b", true},
		{[]string{"a", "b", "c"}, "d", false},
		{[]string{}, "a", false},
		{nil, "a", false},
	}
	for _, tt := range tests {
		got := containsStr(tt.slice, tt.s)
		if got != tt.want {
			t.Errorf("containsStr(%v, %q) = %v, want %v", tt.slice, tt.s, got, tt.want)
		}
	}
}

func TestSortResultsByScore(t *testing.T) {
	results := []SearXNGResult{
		{URL: "low", Score: 1.0},
		{URL: "high", Score: 10.0},
		{URL: "mid", Score: 5.0},
	}
	sortResultsByScore(results)
	if results[0].URL != "high" || results[1].URL != "mid" || results[2].URL != "low" {
		t.Errorf("sort order wrong: %v, %v, %v", results[0].URL, results[1].URL, results[2].URL)
	}
}

func TestSortResultsByScore_EqualScores(t *testing.T) {
	results := []SearXNGResult{
		{URL: "a", Score: 5.0},
		{URL: "b", Score: 5.0},
		{URL: "c", Score: 5.0},
	}
	sortResultsByScore(results)
	for _, r := range results {
		if r.Score != 5.0 {
			t.Fatalf("score changed during sort")
		}
	}
}

func TestSortResultsByScore_Empty(t *testing.T) {
	var results []SearXNGResult
	sortResultsByScore(results)
}

func TestMergeResults_Deduplication(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://example.com/page", Title: "Page", Content: "short", Engines: []string{"google"}, Score: 3.0},
			},
		}},
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://www.example.com/page/", Title: "Page", Content: "longer content here", Engines: []string{"bing"}, Score: 2.0},
			},
		}},
	}

	merged := mergeResults(fan, "test")

	if len(merged.Results) != 1 {
		t.Fatalf("expected 1 deduplicated result, got %d", len(merged.Results))
	}

	r := merged.Results[0]
	if r.Score != 5.0 {
		t.Errorf("expected accumulated score 5.0, got %f", r.Score)
	}
	if len(r.Engines) != 2 {
		t.Errorf("expected 2 engines, got %d: %v", len(r.Engines), r.Engines)
	}
	if r.Content != "longer content here" {
		t.Errorf("expected longer content to win, got %q", r.Content)
	}
}

func TestMergeResults_PreservesOrder(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://first.com", Score: 1.0, Engines: []string{"a"}},
				{URL: "https://second.com", Score: 10.0, Engines: []string{"a"}},
			},
		}},
	}

	merged := mergeResults(fan, "test")

	if len(merged.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(merged.Results))
	}
	if merged.Results[0].URL != "https://second.com" {
		t.Errorf("expected highest-score result first, got %q", merged.Results[0].URL)
	}
}

func TestMergeResults_SkipsErrors(t *testing.T) {
	fan := []fanResult{
		{err: fmt.Errorf("network error")},
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://ok.com", Score: 1.0, Engines: []string{"a"}},
			},
		}},
		{resp: nil, err: nil},
	}

	merged := mergeResults(fan, "test")

	if len(merged.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(merged.Results))
	}
}

func TestMergeResults_DedupsAnswersAndSuggestions(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Answers:     []any{"42"},
			Suggestions: []any{"suggestion1"},
		}},
		{resp: &SearXNGResponse{
			Answers:     []any{"42", "other"},
			Suggestions: []any{"suggestion1", "suggestion2"},
		}},
	}

	merged := mergeResults(fan, "test")

	if len(merged.Answers) != 2 {
		t.Errorf("expected 2 unique answers, got %d: %v", len(merged.Answers), merged.Answers)
	}
	if len(merged.Suggestions) != 2 {
		t.Errorf("expected 2 unique suggestions, got %d: %v", len(merged.Suggestions), merged.Suggestions)
	}
}

func TestMergeResults_DedupsInfoboxes(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Infoboxes: []any{map[string]any{"id": "wiki-1", "title": "Test"}},
		}},
		{resp: &SearXNGResponse{
			Infoboxes: []any{map[string]any{"id": "wiki-1", "title": "Test"}},
		}},
	}

	merged := mergeResults(fan, "test")

	if len(merged.Infoboxes) != 1 {
		t.Errorf("expected 1 unique infobox, got %d", len(merged.Infoboxes))
	}
}

func TestMergeResults_EngineDedup(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://example.com", Engines: []string{"google", "bing"}, Score: 1.0},
			},
		}},
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://example.com", Engines: []string{"bing", "duckduckgo"}, Score: 1.0},
			},
		}},
	}

	merged := mergeResults(fan, "test")

	r := merged.Results[0]
	if len(r.Engines) != 3 {
		t.Errorf("expected 3 unique engines, got %d: %v", len(r.Engines), r.Engines)
	}
}

func TestMergeResults_Empty(t *testing.T) {
	merged := mergeResults(nil, "test")
	if len(merged.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(merged.Results))
	}
	if merged.NumberOfResults != 0 {
		t.Errorf("expected NumberOfResults=0, got %d", merged.NumberOfResults)
	}
}

func TestMergeResults_NumberOfResults(t *testing.T) {
	fan := []fanResult{
		{resp: &SearXNGResponse{
			Results: []SearXNGResult{
				{URL: "https://a.com", Score: 1.0, Engines: []string{"a"}},
				{URL: "https://b.com", Score: 2.0, Engines: []string{"a"}},
			},
		}},
	}

	merged := mergeResults(fan, "test")

	if merged.NumberOfResults != 2 {
		t.Errorf("expected NumberOfResults=2, got %d", merged.NumberOfResults)
	}
}
