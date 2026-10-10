package budget

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// DefaultsSeed names the one-time seed of the default categories and
// merchant rules in finance_seeds.
const DefaultsSeed = "budget_defaults_v1"

// DefaultRulePriority is the first default merchant rule's priority; they
// come after every rule the owner adds without a priority.
const DefaultRulePriority = 100000

// DefaultCategory is a category Finance adds once, unless a category of
// its name exists.
type DefaultCategory struct {
	Key, Name, Kind, Color string
}

// DefaultCategories are the budget's starting categories.
var DefaultCategories = []DefaultCategory{
	{rules.KeyGroceries, "Groceries", KindExpense, "#22c55e"},
	{rules.KeyEatingOut, "Eating out", KindExpense, "#f97316"},
	{"housing", "Housing & rent", KindExpense, "#78716c"},
	{rules.KeyUtilities, "Utilities & phone", KindExpense, "#14b8a6"},
	{rules.KeyTransport, "Transport", KindExpense, "#3b82f6"},
	{rules.KeySubscriptions, "Subscriptions", KindExpense, "#8b5cf6"},
	{rules.KeyShopping, "Shopping", KindExpense, "#ec4899"},
	{rules.KeyHealth, "Health & care", KindExpense, "#ef4444"},
	{rules.KeyFun, "Fun & leisure", KindExpense, "#eab308"},
	{KeySavings, "Savings", KindTransfer, ""},
	{"income", "Income", KindIncome, ""},
	{"transfers", "Transfers", KindTransfer, ""},
	{"other", "Other", KindExpense, ""},
}

// DefaultRule is a merchant rule Finance adds once, as an ordinary rule.
type DefaultRule struct {
	Key, Field, Operator, Text string
}

func counterparty(key, text string) DefaultRule {
	return DefaultRule{key, rules.FieldCounterparty, rules.OperatorContains, text}
}

func exactly(key, text string) DefaultRule {
	return DefaultRule{key, rules.FieldCounterparty, rules.OperatorEquals, text}
}

func anywhere(key, text string) DefaultRule {
	return DefaultRule{key, rules.FieldAny, rules.OperatorContains, text}
}

// DefaultRules are common German merchants. Short names that occur inside
// other words ("dm", "Aldi", "Esso" in "Espresso") match exactly or with
// more of the name; subscriptions also match the description, since they
// are often paid through PayPal or Google.
var DefaultRules = []DefaultRule{
	counterparty(rules.KeyGroceries, "REWE"),
	counterparty(rules.KeyGroceries, "EDEKA"),
	counterparty(rules.KeyGroceries, "Lidl"),
	exactly(rules.KeyGroceries, "Aldi"),
	counterparty(rules.KeyGroceries, "Aldi Süd"),
	counterparty(rules.KeyGroceries, "Aldi Sued"),
	counterparty(rules.KeyGroceries, "Aldi Nord"),
	counterparty(rules.KeyGroceries, "Penny"),
	counterparty(rules.KeyGroceries, "Netto"),
	counterparty(rules.KeyGroceries, "Kaufland"),
	exactly(rules.KeyGroceries, "dm"),
	counterparty(rules.KeyGroceries, "dm-drogerie"),
	counterparty(rules.KeyGroceries, "dm drogerie"),
	counterparty(rules.KeyGroceries, "Rossmann"),
	counterparty(rules.KeyTransport, "Deutsche Bahn"),
	counterparty(rules.KeyTransport, "DB Vertrieb"),
	counterparty(rules.KeyTransport, "DB Fernverkehr"),
	counterparty(rules.KeyTransport, "RMV"),
	counterparty(rules.KeyTransport, "DADINA"),
	counterparty(rules.KeyTransport, "HEAG mobilo"),
	counterparty(rules.KeyTransport, "Shell"),
	counterparty(rules.KeyTransport, "Aral"),
	exactly(rules.KeyTransport, "Esso"),
	counterparty(rules.KeyTransport, "Esso Station"),
	counterparty(rules.KeyTransport, "Esso Tankstelle"),
	anywhere(rules.KeySubscriptions, "Netflix"),
	anywhere(rules.KeySubscriptions, "Spotify"),
	anywhere(rules.KeySubscriptions, "Disney+"),
	anywhere(rules.KeySubscriptions, "Disney Plus"),
	anywhere(rules.KeySubscriptions, "Amazon Prime"),
	anywhere(rules.KeySubscriptions, "YouTube Premium"),
}

// SeedStore is what Seed needs from the repository.
type SeedStore interface {
	LockSeeds(ctx context.Context) error
	SeedApplied(ctx context.Context, name string) (bool, error)
	MarkSeedApplied(ctx context.Context, name string) error
	GetCategoryBySystemKey(ctx context.Context, systemKey string) (store.FinancialCategory, error)
	AdoptCategory(ctx context.Context, arg store.AdoptCategoryParams) (store.FinancialCategory, error)
	CategoryNameTaken(ctx context.Context, name string) (bool, error)
	CreateDefaultCategory(ctx context.Context, arg store.CreateDefaultCategoryParams) (store.FinancialCategory, error)
	ListCategoryRules(ctx context.Context) ([]store.CategoryRule, error)
	CreateCategoryRule(ctx context.Context, arg store.CreateCategoryRuleParams) (store.CategoryRule, error)
}

// SeedResult counts what Seed did; Applied is false when the seed had run
// before and nothing was done.
type SeedResult struct {
	Applied                                       bool
	CategoriesCreated, CategoriesAdopted          int
	CategoriesSkipped, RulesCreated, RulesSkipped int
}

// Seed adds the default categories and merchant rules once (run it in a
// transaction). A default category is created only when no category has
// its name in any case; an existing one of the same name and kind becomes
// the default one, keeping its name, color, and transactions; one of
// another kind is left alone and the default is skipped. A default rule is
// skipped when its category was skipped or a rule with the same match
// exists.
func Seed(ctx context.Context, q SeedStore) (SeedResult, error) {
	var result SeedResult
	if err := q.LockSeeds(ctx); err != nil {
		return result, err
	}
	if applied, err := q.SeedApplied(ctx, DefaultsSeed); err != nil || applied {
		return result, err
	}
	result.Applied = true
	ids := map[string]store.FinancialCategory{}
	for _, def := range DefaultCategories {
		category, err := q.GetCategoryBySystemKey(ctx, def.Key)
		if err == nil {
			ids[def.Key] = category
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		category, err = q.AdoptCategory(ctx, store.AdoptCategoryParams{SystemKey: def.Key, Name: def.Name, Kind: def.Kind})
		if err == nil {
			ids[def.Key] = category
			result.CategoriesAdopted++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		taken, err := q.CategoryNameTaken(ctx, def.Name)
		if err != nil {
			return result, err
		}
		if taken {
			result.CategoriesSkipped++
			continue
		}
		var color *string
		if def.Color != "" {
			color = &def.Color
		}
		category, err = q.CreateDefaultCategory(ctx, store.CreateDefaultCategoryParams{Name: def.Name, Kind: def.Kind, Color: color, SystemKey: def.Key})
		if err != nil {
			return result, fmt.Errorf("create default category %s: %w", def.Name, err)
		}
		ids[def.Key] = category
		result.CategoriesCreated++
	}
	existing, err := q.ListCategoryRules(ctx)
	if err != nil {
		return result, err
	}
	known := map[string]bool{}
	for _, rule := range existing {
		known[ruleIdentity(rule.MatchField, rule.MatchOperator, rule.MatchText)] = true
	}
	for index, def := range DefaultRules {
		category, ok := ids[def.Key]
		identity := ruleIdentity(def.Field, def.Operator, def.Text)
		if !ok || known[identity] {
			result.RulesSkipped++
			continue
		}
		priority := int32(DefaultRulePriority + 10*index)
		if _, err := q.CreateCategoryRule(ctx, store.CreateCategoryRuleParams{CategoryID: category.ID, MatchField: def.Field,
			MatchOperator: def.Operator, MatchText: def.Text, Priority: &priority}); err != nil {
			return result, fmt.Errorf("create default rule %q: %w", def.Text, err)
		}
		known[identity] = true
		result.RulesCreated++
	}
	return result, q.MarkSeedApplied(ctx, DefaultsSeed)
}

func ruleIdentity(field, operator, text string) string {
	return field + "\x1f" + operator + "\x1f" + rules.Normalize(text)
}
