package api

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

var (
	categoryKinds = map[string]bool{"expense": true, "income": true, "transfer": true}
	colorPattern  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

const categoryUnique = "financial_categories_name_kind_key"

func categoryView(item store.FinancialCategory) generated.Category {
	view := generated.Category{Id: uuid(item.ID), Name: item.Name, Kind: generated.CategoryKind(item.Kind), Color: item.Color,
		Archived: item.ArchivedAt.Valid, ArchivedAt: nullableTime(item.ArchivedAt), CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC()}
	if item.SystemKey != nil {
		view.SystemKey = pointer(generated.CategorySystemKey(*item.SystemKey))
	}
	return view
}

func ruleView(item store.CategoryRule) generated.CategoryRule {
	return generated.CategoryRule{Id: uuid(item.ID), CategoryId: uuid(item.CategoryID), Priority: int(item.Priority),
		Match:     generated.RuleMatch{Field: generated.RuleField(item.MatchField), Operator: generated.RuleOperator(item.MatchOperator), Text: item.MatchText},
		CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC()}
}

// categorizer is the rules in the order they are tried, with the active
// default categories for the merchant category code fallback.
func categorizer(ctx context.Context, q *store.Queries) (rules.Categorizer, error) {
	active, err := q.ListActiveCategoryRules(ctx)
	if err != nil {
		return rules.Categorizer{}, err
	}
	defaults, err := q.ListDefaultCategories(ctx)
	if err != nil {
		return rules.Categorizer{}, err
	}
	result := rules.Categorizer{Rules: ruleSet(active), Defaults: make(map[string][16]byte, len(defaults))}
	for _, category := range defaults {
		result.Defaults[*category.SystemKey] = category.ID.Bytes
	}
	return result, nil
}

func categoryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", invalid("name must be 1 to 100 characters")
	}
	return name, nil
}

func categoryExists(name string) error {
	return apiError(fiber.StatusConflict, "category_exists", "A category of this kind is already called "+name)
}

func (h *Handler) ListCategories(c fiber.Ctx, params generated.ListCategoriesParams) error {
	items, err := h.queries.ListFinancialCategories(c.Context(), params.IncludeArchived != nil && *params.IncludeArchived)
	if err != nil {
		return internal(err)
	}
	result := generated.CategoryList{Items: make([]generated.Category, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, categoryView(item))
	}
	return c.JSON(result)
}

func (h *Handler) CreateCategory(c fiber.Ctx) error {
	var body generated.CreateCategoryRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	name, err := categoryName(body.Name)
	if err != nil {
		return err
	}
	if !categoryKinds[string(body.Kind)] {
		return invalid("kind must be expense, income, or transfer")
	}
	if body.Color != nil && !colorPattern.MatchString(*body.Color) {
		return invalid("color must be #RRGGBB")
	}
	item, err := h.queries.CreateFinancialCategory(c.Context(), store.CreateFinancialCategoryParams{Name: name, Kind: string(body.Kind), Color: body.Color})
	if uniqueViolation(err, categoryUnique) {
		return categoryExists(name)
	}
	if err != nil {
		return internal(err)
	}
	h.recordCategory(c, categoryCreated, item, nil)
	return c.Status(fiber.StatusCreated).JSON(categoryView(item))
}

func (h *Handler) UpdateCategory(c fiber.Ctx, categoryID types.UUID) error {
	var body generated.UpdateCategoryRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	params := store.UpdateFinancialCategoryParams{ID: pgUUID(categoryID), Archived: body.Archived}
	known := 0
	for _, name := range []string{"name", "kind", "color", "archived"} {
		if _, ok := properties[name]; ok {
			if name != "color" && isNull(properties, name) {
				return invalid(name + " cannot be null")
			}
			known++
		}
	}
	if known == 0 {
		return invalid("the update names no category field")
	}
	if body.Name != nil {
		name, err := categoryName(*body.Name)
		if err != nil {
			return err
		}
		params.Name = &name
	}
	if body.Kind != nil {
		if !categoryKinds[string(*body.Kind)] {
			return invalid("kind must be expense, income, or transfer")
		}
		params.Kind = pointer(string(*body.Kind))
	}
	if _, ok := properties["color"]; ok {
		if body.Color != nil && !colorPattern.MatchString(*body.Color) {
			return invalid("color must be #RRGGBB or null")
		}
		params.SetColor, params.Color = true, body.Color
	}
	var before, after store.FinancialCategory
	err = h.inTx(c.Context(), func(q *store.Queries) error {
		var err error
		before, err = q.GetFinancialCategory(c.Context(), params.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Category")
		}
		if err != nil {
			return err
		}
		after, err = q.UpdateFinancialCategory(c.Context(), params)
		if uniqueViolation(err, categoryUnique) {
			name := before.Name
			if params.Name != nil {
				name = *params.Name
			}
			return categoryExists(name)
		}
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	var changes []string
	add := func(name string, changed bool) {
		if changed {
			changes = append(changes, name)
		}
	}
	add("name", before.Name != after.Name)
	add("kind", before.Kind != after.Kind)
	add("color", !sameText(before.Color, after.Color))
	add("archived", before.ArchivedAt.Valid != after.ArchivedAt.Valid)
	switch {
	case len(changes) == 0:
	case !before.ArchivedAt.Valid && after.ArchivedAt.Valid:
		h.recordCategory(c, categoryArchived, after, changes)
	case before.ArchivedAt.Valid && !after.ArchivedAt.Valid:
		h.recordCategory(c, categoryRestored, after, changes)
	default:
		h.recordCategory(c, categoryUpdated, after, changes)
	}
	if len(changes) > 0 {
		h.budgetChanged()
	}
	return c.JSON(categoryView(after))
}

func (h *Handler) ListCategoryRules(c fiber.Ctx) error {
	items, err := h.queries.ListCategoryRules(c.Context())
	if err != nil {
		return internal(err)
	}
	result := generated.CategoryRuleList{Items: make([]generated.CategoryRule, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, ruleView(item))
	}
	return c.JSON(result)
}

// ruleMatch validates a rule's match and returns its stored form.
func ruleMatch(match generated.RuleMatch) (field, operator, text string, err error) {
	field, operator = string(match.Field), string(match.Operator)
	if !rules.ValidField(field) {
		return "", "", "", invalid("match.field must be counterparty, description, or any")
	}
	if !rules.ValidOperator(operator) {
		return "", "", "", invalid("match.operator must be contains or equals")
	}
	text = strings.TrimSpace(match.Text)
	if text == "" || utf8.RuneCountInString(text) > 200 {
		return "", "", "", invalid("match.text must be 1 to 200 characters")
	}
	return field, operator, text, nil
}

func (h *Handler) CreateCategoryRule(c fiber.Ctx) error {
	var body generated.CreateCategoryRuleRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["match"]; !ok {
		return invalid("match is required")
	}
	if _, ok := properties["categoryId"]; !ok {
		return invalid("categoryId is required")
	}
	field, operator, text, err := ruleMatch(body.Match)
	if err != nil {
		return err
	}
	if _, err := usableCategory(c.Context(), h.queries, pgUUID(body.CategoryId)); err != nil {
		return passThrough(err)
	}
	var priority *int32
	if body.Priority != nil {
		priority = pointer(int32(*body.Priority))
	}
	item, err := h.queries.CreateCategoryRule(c.Context(), store.CreateCategoryRuleParams{CategoryID: pgUUID(body.CategoryId), MatchField: field, MatchOperator: operator, MatchText: text, Priority: priority})
	if err != nil {
		return internal(err)
	}
	h.recordRule(c, ruleCreated, item)
	return c.Status(fiber.StatusCreated).JSON(ruleView(item))
}

func (h *Handler) UpdateCategoryRule(c fiber.Ctx, ruleID types.UUID) error {
	var body generated.UpdateCategoryRuleRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	known := 0
	for _, name := range []string{"categoryId", "match", "priority"} {
		if _, ok := properties[name]; ok {
			if isNull(properties, name) {
				return invalid(name + " cannot be null")
			}
			known++
		}
	}
	if known == 0 {
		return invalid("the update names no rule field")
	}
	params := store.UpdateCategoryRuleParams{ID: pgUUID(ruleID)}
	if body.Match != nil {
		field, operator, text, err := ruleMatch(*body.Match)
		if err != nil {
			return err
		}
		params.MatchField, params.MatchOperator, params.MatchText = &field, &operator, &text
	}
	if body.Priority != nil {
		params.Priority = pointer(int32(*body.Priority))
	}
	var item store.CategoryRule
	err = h.inTx(c.Context(), func(q *store.Queries) error {
		before, err := q.GetCategoryRule(c.Context(), params.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Rule")
		}
		if err != nil {
			return err
		}
		if body.CategoryId != nil {
			params.CategoryID = pgUUID(*body.CategoryId)
			if params.CategoryID != before.CategoryID {
				if _, err := usableCategory(c.Context(), q, params.CategoryID); err != nil {
					return err
				}
			}
		}
		item, err = q.UpdateCategoryRule(c.Context(), params)
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	h.recordRule(c, ruleUpdated, item)
	return c.JSON(ruleView(item))
}

func (h *Handler) DeleteCategoryRule(c fiber.Ctx, ruleID types.UUID) error {
	item, err := h.queries.DeleteCategoryRule(c.Context(), pgUUID(ruleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Rule")
	}
	if err != nil {
		return internal(err)
	}
	h.recordRule(c, ruleDeleted, item)
	return c.SendStatus(fiber.StatusNoContent)
}

// ruleCounts are the outcomes of re-applying the rules.
type ruleCounts struct {
	Examined, Categorized, Recategorized, Cleared, Unchanged int
}

func (h *Handler) ApplyCategoryRules(c fiber.Ctx) error {
	scope := "uncategorized"
	if raw := strings.TrimSpace(string(c.Body())); raw != "" {
		var body generated.ApplyCategoryRulesRequest
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			return invalid("the body must be a JSON object")
		}
		if body.Scope != nil {
			scope = string(*body.Scope)
		}
	}
	if scope != "uncategorized" && scope != "all" {
		return invalid("scope must be uncategorized or all")
	}
	ctx := c.Context()
	var counts ruleCounts
	err := h.inTx(ctx, func(q *store.Queries) error {
		chooser, err := categorizer(ctx, q)
		if err != nil {
			return err
		}
		candidates, err := q.ListRuleCandidates(ctx, scope == "all")
		if err != nil {
			return err
		}
		update := store.SetRuleCategoriesParams{Ids: []pgtype.UUID{}, CategoryIds: []pgtype.UUID{}, RuleIds: []pgtype.UUID{}, CategorizedBy: []string{}}
		for _, candidate := range candidates {
			counts.Examined++
			current := store.Transaction{CategoryID: candidate.CategoryID, CategoryRuleID: candidate.CategoryRuleID, CategorizedBy: candidate.CategorizedBy}
			assignment, outcome := chooser.Reapply(assignmentOf(current), textWithCode(candidate.Counterparty, candidate.Description, candidate.MerchantCategoryCode))
			switch outcome {
			case rules.Categorized:
				counts.Categorized++
			case rules.Recategorized:
				counts.Recategorized++
			case rules.Cleared:
				counts.Cleared++
			default:
				counts.Unchanged++
			}
			next := current
			applyAssignment(&next, assignment)
			if next.CategoryID == current.CategoryID && next.CategoryRuleID == current.CategoryRuleID && sameText(next.CategorizedBy, current.CategorizedBy) {
				continue
			}
			update.Ids = append(update.Ids, candidate.ID)
			update.CategoryIds = append(update.CategoryIds, next.CategoryID)
			update.RuleIds = append(update.RuleIds, next.CategoryRuleID)
			update.CategorizedBy = append(update.CategorizedBy, assignment.CategorizedBy)
		}
		if len(update.Ids) == 0 {
			return nil
		}
		return q.SetRuleCategories(ctx, update)
	})
	if err != nil {
		return internal(err)
	}
	h.recordRulesApplied(c, scope, counts)
	if counts.Categorized+counts.Recategorized+counts.Cleared > 0 {
		h.budgetChanged()
	}
	return c.JSON(generated.ApplyCategoryRulesResult{Scope: generated.ApplyScope(scope), Examined: counts.Examined, Categorized: counts.Categorized,
		Recategorized: counts.Recategorized, Cleared: counts.Cleared, Unchanged: counts.Unchanged})
}
