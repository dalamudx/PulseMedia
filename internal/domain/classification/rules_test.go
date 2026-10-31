package classification

import (
	"github.com/google/uuid"
	"testing"
)

func TestFiniteClassificationUnknownConflict(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	rules := []Rule{{ID: "one", MediaType: "movie", RootID: a, Genres: []string{"Drama"}, Languages: []string{"en"}, Countries: []string{"US"}}}
	if e := Validate(rules); e != nil {
		t.Fatal(e)
	}
	f := Facts{MediaType: "movie", Genres: []string{"drama"}, Languages: []string{"en"}, Countries: []string{"US"}}
	r := Evaluate(rules, f)
	if r.Status != "matched" || r.RootID == nil || *r.RootID != a || len(r.Matches[0].Reasons) != 4 {
		t.Fatal(r)
	}
	f.Languages = nil
	if r = Evaluate(rules, f); r.Status != "insufficient_evidence" {
		t.Fatal(r)
	}
	f.Languages = []string{}
	if r = Evaluate(rules, f); r.Status != "no_match" {
		t.Fatal(r)
	}
	f.Languages = []string{"en"}
	rules = append(rules, Rule{ID: "two", MediaType: "movie", RootID: b})
	if r = Evaluate(rules, f); r.Status != "conflict" || r.RootID != nil {
		t.Fatal(r)
	}
	rules[1].RootID = a
	if r = Evaluate(rules, f); r.Status != "matched" {
		t.Fatal(r)
	}
	rules[0].MediaType = "anime"
	if Validate(rules) == nil {
		t.Fatal("dynamic type accepted")
	}
}
