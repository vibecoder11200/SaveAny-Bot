package handlers

import "testing"

// The search grammar keeps a trailing bare number as query text ("report
// 2024"); only the explicit "p<digits>" marker selects a page.
func TestHistorySearchArgs(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		query string
		page  int
	}{
		{"plain query", []string{"report"}, "report", 1},
		{"trailing number stays query", []string{"report", "2024"}, "report 2024", 1},
		{"marker sets page", []string{"report", "p2"}, "report", 2},
		{"multi token with marker", []string{"photos", "from", "alice", "p3"}, "photos from alice", 3},
		{"lone marker is a query", []string{"p2"}, "p2", 1},
		{"p0 is not a page", []string{"report", "p0"}, "report p0", 1},
		{"uppercase P stays query", []string{"report", "P2"}, "report P2", 1},
		{"bare p stays query", []string{"report", "p"}, "report p", 1},
		{"empty tail", nil, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query, page := historySearchArgs(tc.args)
			if query != tc.query || page != tc.page {
				t.Fatalf("historySearchArgs(%q) = %q,%d want %q,%d", tc.args, query, page, tc.query, tc.page)
			}
		})
	}
}

func TestHistoryPageMarker(t *testing.T) {
	valid := map[string]int{"p1": 1, "p2": 2, "p10": 10, "p999": 999}
	for token, want := range valid {
		if page, ok := historyPageMarker(token); !ok || page != want {
			t.Errorf("historyPageMarker(%q) = %d,%v want %d,true", token, page, ok, want)
		}
	}
	for _, token := range []string{"", "p", "p0", "p-1", "P2", "2", "px", "p2x", "pp2"} {
		if _, ok := historyPageMarker(token); ok {
			t.Errorf("historyPageMarker(%q) must not parse as a marker", token)
		}
	}
}
