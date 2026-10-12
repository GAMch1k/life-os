// Package rules is Finance's category rules engine: a rule gives a
// transaction a category when text in its counterparty or description
// matches, and a card transaction no rule matches may get the default
// category of its merchant category code. Who chose a category ranks: a
// choice by hand, then a rule, then a code, then the AI, which fills the
// gaps the others leave. It is pure; callers load the rules (in priority
// order) and the default categories, and store the outcome.
package rules

import "strings"

// Fields a rule can match.
const (
	FieldCounterparty = "counterparty"
	FieldDescription  = "description"
	FieldAny          = "any"
)

// Operators a rule can use.
const (
	OperatorContains = "contains"
	OperatorEquals   = "equals"
)

// Rule is one stored rule. ID and CategoryID are opaque to the engine.
type Rule struct {
	ID         [16]byte
	CategoryID [16]byte
	Field      string
	Operator   string
	Text       string
}

// Text is what a rule is matched against. MerchantCategoryCode is the
// card's code a bank reported, for the fallback.
type Text struct {
	Counterparty         string
	Description          string
	MerchantCategoryCode string
}

// Who chose a category.
const (
	ByManual = "manual"
	ByRule   = "rule"
	ByMCC    = "mcc"
	ByAI     = "ai"
)

// ValidField and ValidOperator check stored or requested values.
func ValidField(field string) bool {
	return field == FieldCounterparty || field == FieldDescription || field == FieldAny
}

func ValidOperator(operator string) bool {
	return operator == OperatorContains || operator == OperatorEquals
}

// Normalize is the comparable form of text: lower case, with runs of
// whitespace collapsed and surrounding whitespace removed.
func Normalize(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// Matches reports whether the rule matches the text: case-insensitively,
// ignoring surrounding and repeated whitespace. A rule with empty text
// matches nothing.
func (r Rule) Matches(text Text) bool {
	want := Normalize(r.Text)
	if want == "" {
		return false
	}
	match := func(value string) bool {
		value = Normalize(value)
		if r.Operator == OperatorEquals {
			return value == want
		}
		return strings.Contains(value, want)
	}
	switch r.Field {
	case FieldCounterparty:
		return match(text.Counterparty)
	case FieldDescription:
		return match(text.Description)
	case FieldAny:
		return match(text.Counterparty) || match(text.Description)
	}
	return false
}

// First returns the first rule (in the given order: priority, then age)
// that matches the text.
func First(rules []Rule, text Text) (Rule, bool) {
	for _, rule := range rules {
		if rule.Matches(text) {
			return rule, true
		}
	}
	return Rule{}, false
}

// Assignment is a transaction's category state as far as rules are
// concerned.
type Assignment struct {
	CategoryID    *[16]byte
	RuleID        *[16]byte
	CategorizedBy string // "", "manual", "rule", "mcc", or "ai"
}

// Outcome classifies what re-applying the rules does to one transaction.
type Outcome int

const (
	Unchanged Outcome = iota
	Categorized
	Recategorized
	Cleared
)

// Categorizer chooses the category of a transaction whose category was not
// chosen by hand: the first rule that matches, else the default category
// for its merchant category code.
type Categorizer struct {
	// Rules in the order they are tried.
	Rules []Rule
	// Defaults maps a default category's system key (such as groceries) to
	// the category; a key without an active category gives none.
	Defaults map[string][16]byte
}

// Choose returns the category the rules, else the merchant category code,
// give the text; false when neither does.
func (c Categorizer) Choose(text Text) (Assignment, bool) {
	if rule, ok := First(c.Rules, text); ok {
		return Assignment{CategoryID: &rule.CategoryID, RuleID: &rule.ID, CategorizedBy: ByRule}, true
	}
	if key, ok := MerchantGroup(text.MerchantCategoryCode); ok {
		if category, ok := c.Defaults[key]; ok {
			return Assignment{CategoryID: &category, CategorizedBy: ByMCC}, true
		}
	}
	return Assignment{}, false
}

// Reapply computes a transaction's new assignment. A choice by hand
// (CategorizedBy "manual") is never changed. An AI choice gives way to a
// rule or a code and otherwise stays. A transaction nothing matches keeps
// no category; one a rule or a code categorized loses it when nothing
// matches any more.
func (c Categorizer) Reapply(current Assignment, text Text) (Assignment, Outcome) {
	if current.CategorizedBy == ByManual {
		return current, Unchanged
	}
	next, ok := c.Choose(text)
	switch {
	case !ok && current.CategorizedBy == ByAI:
		return current, Unchanged
	case !ok && current.CategoryID == nil:
		return Assignment{}, Unchanged
	case !ok:
		return Assignment{}, Cleared
	case current.CategoryID == nil:
		return next, Categorized
	case *current.CategoryID != *next.CategoryID:
		return next, Recategorized
	}
	// Same category, now attributed to whatever chose it first.
	return next, Unchanged
}

// Rechoose is Reapply for a transaction whose text (or code) changed: an
// AI choice that no rule or code replaces is dropped too, since the AI chose
// it from the old text, so the AI looks at the new one.
func (c Categorizer) Rechoose(current Assignment, text Text) (Assignment, Outcome) {
	next, outcome := c.Reapply(current, text)
	if next.CategorizedBy == ByAI {
		return Assignment{}, Cleared
	}
	return next, outcome
}

// Reapply is Categorizer{Rules: rules}.Reapply: rules only.
func Reapply(rules []Rule, current Assignment, text Text) (Assignment, Outcome) {
	return Categorizer{Rules: rules}.Reapply(current, text)
}
