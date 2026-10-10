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

func TestMerchantCategoryCodesMapToTheDefaultCategories(t *testing.T) {
	cases := map[string]string{
		"5411": KeyGroceries, "5499": KeyGroceries, " 5462 ": KeyGroceries,
		"5812": KeyEatingOut, "5814": KeyEatingOut,
		"4111": KeyTransport, "4112": KeyTransport, "4121": KeyTransport, "4131": KeyTransport, "4784": KeyTransport, "4789": KeyTransport,
		"5541": KeyTransport, "5542": KeyTransport, "7523": KeyTransport,
		"4899": KeySubscriptions, "5815": KeySubscriptions, "5818": KeySubscriptions,
		"4812": KeyUtilities, "4814": KeyUtilities, "4900": KeyUtilities,
		"5912": KeyHealth, "5122": KeyHealth, "8011": KeyHealth, "8099": KeyHealth,
		"7832": KeyFun, "7922": KeyFun, "7991": KeyFun, "7999": KeyFun,
		"5200": KeyShopping, "5311": KeyShopping, "5399": KeyShopping, "5651": KeyShopping, "5732": KeyShopping, "5734": KeyShopping,
		"5940": KeyShopping, "5999": KeyShopping,
	}
	for code, want := range cases {
		if got, ok := MerchantGroup(code); !ok || got != want {
			t.Errorf("MerchantGroup(%q) = %q %v, want %q", code, got, ok, want)
		}
	}
	for _, code := range []string{"", "541", "54111", "abcd", "-541", "6011", "4829", "5813x", "5733", "5911", "5939"} {
		if got, ok := MerchantGroup(code); ok {
			t.Errorf("MerchantGroup(%q) = %q, want none", code, got)
		}
	}
}

func TestTheMerchantCategoryCodeIsTriedAfterTheRules(t *testing.T) {
	groceries, dining, shopping := id(10), id(20), id(30)
	rule := Rule{ID: id(1), CategoryID: dining, Field: FieldCounterparty, Operator: OperatorContains, Text: "bistro"}
	c := Categorizer{Rules: []Rule{rule}, Defaults: map[string][16]byte{KeyGroceries: groceries, KeyShopping: shopping}}

	// A rule wins over the code.
	if got, ok := c.Choose(Text{Counterparty: "Bistro am Markt", MerchantCategoryCode: "5411"}); !ok || *got.CategoryID != dining || got.CategorizedBy != ByRule {
		t.Fatalf("rule first: %+v %v", got, ok)
	}
	got, ok := c.Choose(Text{Counterparty: "Markthalle", MerchantCategoryCode: "5411"})
	if !ok || *got.CategoryID != groceries || got.CategorizedBy != ByMCC || got.RuleID != nil {
		t.Fatalf("code fallback: %+v %v", got, ok)
	}
	// A code whose default category is missing (or archived) gives none.
	if got, ok := c.Choose(Text{Counterparty: "Kino", MerchantCategoryCode: "7832"}); ok {
		t.Fatalf("no fun category: %+v", got)
	}
	// Re-applying keeps a choice by hand, re-checks a code's choice, and
	// clears it when the code no longer maps.
	if _, outcome := c.Reapply(Assignment{CategoryID: &dining, CategorizedBy: ByManual}, Text{MerchantCategoryCode: "5411"}); outcome != Unchanged {
		t.Fatalf("manual: %v", outcome)
	}
	byCode := Assignment{CategoryID: &groceries, CategorizedBy: ByMCC}
	if next, outcome := c.Reapply(byCode, Text{MerchantCategoryCode: "5311"}); outcome != Recategorized || *next.CategoryID != shopping {
		t.Fatalf("recategorized: %+v %v", next, outcome)
	}
	if next, outcome := c.Reapply(byCode, Text{MerchantCategoryCode: "6011"}); outcome != Cleared || next.CategoryID != nil {
		t.Fatalf("cleared: %+v %v", next, outcome)
	}
	if next, outcome := c.Reapply(Assignment{}, Text{MerchantCategoryCode: "5499"}); outcome != Categorized || next.CategorizedBy != ByMCC {
		t.Fatalf("categorized: %+v %v", next, outcome)
	}
	// A rule that now matches takes over a code's choice of the same
	// category without counting as a change.
	sameRule := Rule{ID: id(2), CategoryID: groceries, Field: FieldCounterparty, Operator: OperatorContains, Text: "markthalle"}
	withRule := Categorizer{Rules: []Rule{sameRule}, Defaults: c.Defaults}
	if next, outcome := withRule.Reapply(byCode, Text{Counterparty: "Markthalle", MerchantCategoryCode: "5411"}); outcome != Unchanged || next.CategorizedBy != ByRule {
		t.Fatalf("rule takes over: %+v %v", next, outcome)
	}
}
