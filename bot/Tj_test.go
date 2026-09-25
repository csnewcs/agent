package main

import (
	"testing"
)

func TestSearchTJAliases(t *testing.T) {
	res, err := SearchTJAliases("処刑拍手")
	if err != nil {
		t.Fatalf("SearchTJAliases failed: %v", err)
	}
	if res.Query != "処刑拍手" {
		t.Errorf("expected query '処刑拍手', got '%s'", res.Query)
	}
	t.Logf("Found %d candidates for query '処刑拍手'", len(res.Candidates))
}

func TestListTJTrackingJSON(t *testing.T) {
	res, err := ListTJTrackingJSON()
	if err != nil {
		t.Fatalf("ListTJTrackingJSON failed: %v", err)
	}
	t.Logf("Today Matches: %d, Artists: %d, Songs: %d",
		len(res.TodayMatches), len(res.Artists), len(res.Songs))
}
