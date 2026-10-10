package rules

import "testing"

func id(b byte) [16]byte { return [16]byte{b} }

func TestRulesMatchCaseInsensitivelyInTheirField(t *testing.T) {
	cases := []struct {
		rule Rule
		text Text
		want bool
	}{
		{Rule{Field: FieldCounterparty, Operator: OperatorContains, Text: "rewe"}, Text{Counterparty: "REWE Markt GmbH"}, true},
		{Rule{Field: FieldCounterparty, Operator: OperatorContains, Text: " REWE "}, Text{Description: "REWE"}, false},
		{Rule{Field: FieldDescription, Operator: OperatorContains, Text: "miete"}, Text{Description: "Miete  Oktober"}, true},
		{Rule{Field: FieldAny, Operator: OperatorContains, Text: "spotify"}, Text{Description: "Spotify AB"}, true},
		{Rule{Field: FieldAny, Operator: OperatorContains, Text: "spotify"}, Text{Counterparty: "SPOTIFY"}, true},
		{Rule{Field: FieldCounterparty, Operator: OperatorEquals, Text: "Bäckerei Schmidt"}, Text{Counterparty: "  bäckerei   SCHMIDT "}, true},
		{Rule{Field: FieldCounterparty, Operator: OperatorEquals, Text: "Bäckerei"}, Text{Counterparty: "Bäckerei Schmidt"}, false},
		{Rule{Field: FieldCounterparty, Operator: OperatorContains, Text: "  "}, Text{Counterparty: "anything"}, false},
		{Rule{Field: "notes", Operator: OperatorContains, Text: "x"}, Text{Counterparty: "x"}, false},
	}
	for index, c := range cases {
		if got := c.rule.Matches(c.text); got != c.want {
			t.Errorf("case %d: %+v on %+v = %v, want %v", index, c.rule, c.text, got, c.want)
		}
	}
}

func TestTheFirstMatchingRuleWins(t *testing.T) {
	ordered := []Rule{
		{ID: id(1), CategoryID: id(10), Field: FieldCounterparty, Operator: OperatorContains, Text: "rewe to go"},
		{ID: id(2), CategoryID: id(20), Field: FieldCounterparty, Operator: OperatorContains, Text: "rewe"},
	}
	if rule, ok := First(ordered, Text{Counterparty: "REWE To Go Hbf"}); !ok || rule.ID != id(1) {
		t.Fatalf("first = %+v %v, want rule 1", rule, ok)
	}
	if rule, ok := First(ordered, Text{Counterparty: "REWE Markt"}); !ok || rule.ID != id(2) {
		t.Fatalf("first = %+v %v, want rule 2", rule, ok)
	}
	if _, ok := First(ordered, Text{Counterparty: "Lidl"}); ok {
		t.Fatal("no rule should match Lidl")
	}
}

func TestReapplyNeverOverridesAChoiceByHand(t *testing.T) {
	groceries, dining := id(10), id(20)
	ruleA, ruleB := id(1), id(2)
	rules := []Rule{{ID: ruleA, CategoryID: groceries, Field: FieldAny, Operator: OperatorContains, Text: "rewe"}}
	text := Text{Counterparty: "REWE"}

	manual := Assignment{CategoryID: &dining, CategorizedBy: "manual"}
	if next, outcome := Reapply(rules, manual, text); outcome != Unchanged || next.CategoryID != &dining {
		t.Fatalf("manual: %+v %v", next, outcome)
	}
	// Cleared by hand stays cleared.
	if next, outcome := Reapply(rules, Assignment{CategorizedBy: "manual"}, text); outcome != Unchanged || next.CategoryID != nil {
		t.Fatalf("manual none: %+v %v", next, outcome)
	}
	next, outcome := Reapply(rules, Assignment{}, text)
	if outcome != Categorized || *next.CategoryID != groceries || *next.RuleID != ruleA || next.CategorizedBy != "rule" {
		t.Fatalf("uncategorized: %+v %v", next, outcome)
	}
	byOtherRule := Assignment{CategoryID: &dining, RuleID: &ruleB, CategorizedBy: "rule"}
	if next, outcome := Reapply(rules, byOtherRule, text); outcome != Recategorized || *next.CategoryID != groceries {
		t.Fatalf("recategorized: %+v %v", next, outcome)
	}
	if next, outcome := Reapply(rules, byOtherRule, Text{Counterparty: "Lidl"}); outcome != Cleared || next.CategoryID != nil || next.CategorizedBy != "" {
		t.Fatalf("cleared: %+v %v", next, outcome)
	}
	same := Assignment{CategoryID: &groceries, RuleID: &ruleA, CategorizedBy: "rule"}
	if _, outcome := Reapply(rules, same, text); outcome != Unchanged {
		t.Fatalf("same: %v", outcome)
	}
}
