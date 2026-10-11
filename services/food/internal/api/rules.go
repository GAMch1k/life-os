package api

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

func ruleView(rule store.AisleRule) generated.AisleRule {
	return generated.AisleRule{Id: uuid(rule.ID), Keyword: rule.Keyword, Aisle: generated.Aisle(rule.Aisle), Match: generated.AisleRuleMatch(rule.Match),
		CreatedAt: rule.CreatedAt.Time.UTC(), CreatedBy: actorOf(rule.CreatedBy)}
}

// aisleRules loads every aisle rule for domain.ChooseAisle.
func aisleRules(ctx context.Context, q *store.Queries) ([]domain.Rule, error) {
	rows, err := q.ListAisleRules(ctx, nil)
	if err != nil {
		return nil, err
	}
	rules := make([]domain.Rule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, domain.Rule{Keyword: row.Keyword, Aisle: row.Aisle, Match: row.Match})
	}
	return rules, nil
}

// aisleFilter checks an optional aisle.
func aisleFilter(value *generated.Aisle) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !value.Valid() {
		return nil, invalid("aisle must be one of the aisles")
	}
	return pointer(string(*value)), nil
}

func (h *Handler) ListAisleRules(c fiber.Ctx, params generated.ListAisleRulesParams) error {
	aisle, err := aisleFilter(params.Aisle)
	if err != nil {
		return err
	}
	rows, err := h.queries.ListAisleRules(c.Context(), aisle)
	if err != nil {
		return internal(err)
	}
	result := generated.AisleRuleList{Items: make([]generated.AisleRule, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, ruleView(row))
	}
	return c.JSON(result)
}

func (h *Handler) CreateAisleRule(c fiber.Ctx, _ generated.CreateAisleRuleParams) error {
	var body generated.CreateAisleRuleRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	_, keyword, err := cleanName("keyword", body.Keyword, keywordLimit)
	if err != nil {
		return err
	}
	aisle, err := aisleFilter(&body.Aisle)
	if err != nil {
		return err
	}
	match := generated.Word
	if body.Match != nil {
		if !body.Match.Valid() {
			return invalid("match must be word or substring")
		}
		match = *body.Match
	}
	params := store.InsertAisleRuleParams{Keyword: keyword, Aisle: *aisle, Match: string(match), Actor: requestActor(c)}
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		rule, err := q.InsertAisleRule(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, apiError(fiber.StatusConflict, "aisle_rule_exists", "A rule already places “"+keyword+"”")
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusCreated, body: ruleView(rule), events: []activity.Event{ruleEvent(aisleRuleCreated, rule)}}, nil
	})
}

func (h *Handler) DeleteAisleRule(c fiber.Ctx, ruleID generated.RuleId, _ generated.DeleteAisleRuleParams) error {
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		rule, err := q.DeleteAisleRule(ctx, pgUUID(ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Aisle rule")
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent, events: []activity.Event{ruleEvent(aisleRuleDeleted, rule)}}, nil
	})
}
