// Package rules is Finance's category rules engine: a rule gives a
// transaction a category when text in its counterparty or description
// matches. It is pure; callers load the rules (in priority order) and store
// the outcome.
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

// Text is what a rule is matched against.
type Text struct {
	Counterparty string
	Description  string
}

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
	CategorizedBy string // "", "manual", or "rule"
}

// Outcome classifies what re-applying the rules does to one transaction.
type Outcome int

const (
	Unchanged Outcome = iota
	Categorized
	Recategorized
	Cleared
)

// Reapply computes a transaction's new assignment from the rules. A choice
// by hand (CategorizedBy "manual") is never changed. A transaction no rule
// matches keeps no category; one a rule categorized loses it when no rule
// matches any more.
func Reapply(rules []Rule, current Assignment, text Text) (Assignment, Outcome) {
	if current.CategorizedBy == "manual" {
		return current, Unchanged
	}
	rule, ok := First(rules, text)
	if !ok {
		if current.CategoryID == nil {
			return Assignment{}, Unchanged
		}
		return Assignment{}, Cleared
	}
	next := Assignment{CategoryID: &rule.CategoryID, RuleID: &rule.ID, CategorizedBy: "rule"}
	switch {
	case current.CategoryID == nil:
		return next, Categorized
	case *current.CategoryID != rule.CategoryID:
		return next, Recategorized
	case current.RuleID == nil || *current.RuleID != rule.ID || current.CategorizedBy != "rule":
		// Same category, now attributed to the rule that matches first.
		return next, Unchanged
	}
	return current, Unchanged
}
