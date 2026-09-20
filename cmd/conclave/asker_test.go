package main

import (
	"testing"

	"github.com/texhik/conclave/internal/tool"
)

func TestParseAnswer(t *testing.T) {
	q := tool.Question{
		Options:     []tool.Option{{Label: "Postgres"}, {Label: "MySQL"}, {Label: "SQLite"}},
		Multiple:    true,
		AllowCustom: true,
	}
	cases := []struct {
		in       string
		selected []string
		custom   string
		wantErr  bool
	}{
		{"2", []string{"MySQL"}, "", false},
		{"1,3", []string{"Postgres", "SQLite"}, "", false},
		{"sql", []string{"SQLite"}, "", false},
		{"cockroachdb", nil, "cockroachdb", false},
		{"", nil, "", false},
		{"9", nil, "", true},
	}
	for _, tc := range cases {
		got, err := parseAnswer(tc.in, q)
		if tc.wantErr != (err != nil) {
			t.Fatalf("parseAnswer(%q) error = %v", tc.in, err)
		}
		if tc.wantErr {
			continue
		}
		if len(got.Selected) != len(tc.selected) {
			t.Fatalf("parseAnswer(%q) selected = %v, want %v", tc.in, got.Selected, tc.selected)
		}
		for i := range tc.selected {
			if got.Selected[i] != tc.selected[i] {
				t.Fatalf("parseAnswer(%q) selected = %v, want %v", tc.in, got.Selected, tc.selected)
			}
		}
		if got.Custom != tc.custom {
			t.Fatalf("parseAnswer(%q) custom = %q, want %q", tc.in, got.Custom, tc.custom)
		}
	}
}

func TestParseAnswerSingleChoiceTruncates(t *testing.T) {
	q := tool.Question{Options: []tool.Option{{Label: "A"}, {Label: "B"}}}
	got, err := parseAnswer("1,2", q)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Selected) != 1 || got.Selected[0] != "A" {
		t.Fatalf("selected = %v, want [A]", got.Selected)
	}
}
