package api

import (
	"errors"
	"sort"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// The AI fills the gaps the rules leave (P13-08). An agent reads the
// candidates, with the owner's choices by hand as examples of their taste,
// and sends its choices back. A choice applies only while no one and
// nothing else chose the transaction's category: a choice by hand, a rule,
// or a merchant category code that landed meanwhile wins, and the rules
// replace the AI's choice later too. The owner's own choice always goes
// through PATCH /v1/transactions/{id} and counts as by hand.

const (
	aiCandidatesDefault = 40
	aiCandidatesMax     = 100
	// aiExamples bounds the owner's choices by hand sent as examples.
	aiExamples   = 50
	aiChoicesMax = 100
)

func (h *Handler) ListAiCategoryCandidates(c fiber.Ctx, params generated.ListAiCategoryCandidatesParams) error {
	limit := aiCandidatesDefault
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > aiCandidatesMax {
			return invalid("limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	ctx := c.Context()
	total, err := h.queries.CountAICategoryCandidates(ctx)
	if err != nil {
		return internal(err)
	}
	result := generated.AiCategoryCandidates{Total: int(total), Items: []generated.AiCategoryCandidate{}, Examples: []generated.AiCategoryExample{}}
	if total > 0 {
		rows, err := h.queries.ListAICategoryCandidates(ctx, int32(limit))
		if err != nil {
			return internal(err)
		}
		for _, row := range rows {
			result.Items = append(result.Items, generated.AiCategoryCandidate{Id: uuid(row.ID), AccountKind: generated.AccountKind(row.AccountKind),
				Amount: money(row.AmountMinor, row.Currency), BookingDate: types.Date{Time: row.OccurredOn.Time},
				Counterparty: row.Counterparty, Description: row.Description})
		}
		examples, err := h.queries.ListManualCategoryExamples(ctx, aiExamples)
		if err != nil {
			return internal(err)
		}
		for _, example := range examples {
			result.Examples = append(result.Examples, generated.AiCategoryExample{Counterparty: example.Counterparty, CategoryId: uuid(example.CategoryID)})
		}
	}
	return c.JSON(result)
}

// aiApplied is what one request of AI choices did, for its Activity event.
type aiApplied struct {
	categorized, unchanged, left, skipped int
	// byCategory counts the categorized transactions per category name.
	byCategory     map[string]int
	transactionIDs []string
}

func (h *Handler) ApplyAiCategoryChoices(c fiber.Ctx) error {
	if actor, ok := activity.ParseActor(c.Get(activity.ActorHeader)); ok && actor.Kind == "user" {
		return apiError(fiber.StatusForbidden, "actor_not_allowed", "The AI's categories are chosen by the AI or a Life OS component; the owner sets a category on the transaction itself")
	}
	var body generated.AiCategoryChoicesRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["choices"]; !ok || len(body.Choices) == 0 || len(body.Choices) > aiChoicesMax {
		return invalid("choices must have 1 to 100 choices")
	}
	ids := make([]pgtype.UUID, 0, len(body.Choices))
	seen := map[types.UUID]bool{}
	for _, choice := range body.Choices {
		if seen[choice.TransactionId] {
			return invalid("a transaction can be chosen for once: " + choice.TransactionId.String())
		}
		seen[choice.TransactionId] = true
		ids = append(ids, pgUUID(choice.TransactionId))
	}
	ctx := c.Context()
	results := make([]generated.AiCategoryChoiceResult, len(body.Choices))
	applied := aiApplied{byCategory: map[string]int{}}
	err = h.inTx(ctx, func(q *store.Queries) error {
		// The rows stay locked until the choices apply, so what is checked
		// here is what they apply to.
		locked, err := q.LockTransactionsForAI(ctx, ids)
		if err != nil {
			return err
		}
		current := make(map[[16]byte]store.LockTransactionsForAIRow, len(locked))
		for _, row := range locked {
			current[row.ID.Bytes] = row
		}
		categories := map[[16]byte]store.FinancialCategory{}
		category := func(id types.UUID) (store.FinancialCategory, bool, error) {
			if found, ok := categories[id]; ok {
				return found, true, nil
			}
			found, err := q.GetFinancialCategory(ctx, pgUUID(id))
			if errors.Is(err, pgx.ErrNoRows) {
				return store.FinancialCategory{}, false, nil
			}
			if err != nil {
				return store.FinancialCategory{}, false, err
			}
			categories[id] = found
			return found, true, nil
		}
		set := store.SetAICategoriesParams{Ids: []pgtype.UUID{}, CategoryIds: []pgtype.UUID{}}
		reviewed := []pgtype.UUID{}
		for index, choice := range body.Choices {
			skip := func(reason generated.AiCategorySkipReason) {
				results[index] = generated.AiCategoryChoiceResult{TransactionId: choice.TransactionId, Outcome: generated.Skipped, Reason: &reason}
				applied.skipped++
			}
			row, ok := current[choice.TransactionId]
			switch {
			case !ok:
				skip(generated.NotFound)
				continue
			case row.CategorizedBy != nil && *row.CategorizedBy != rules.ByAI:
				// A choice by hand (also of none), a rule, or a code.
				skip(generated.Chosen)
				continue
			}
			if choice.CategoryId == nil {
				if row.CategoryID.Valid {
					// The AI's earlier choice stays until something better.
					results[index] = generated.AiCategoryChoiceResult{TransactionId: choice.TransactionId, Outcome: generated.Unchanged}
					applied.unchanged++
					continue
				}
				results[index] = generated.AiCategoryChoiceResult{TransactionId: choice.TransactionId, Outcome: generated.Left}
				applied.left++
				reviewed = append(reviewed, row.ID)
				continue
			}
			chosen, known, err := category(*choice.CategoryId)
			if err != nil {
				return err
			}
			switch {
			case !known:
				skip(generated.UnknownCategory)
				continue
			case chosen.ArchivedAt.Valid:
				skip(generated.CategoryArchived)
				continue
			case (row.AmountMinor < 0 && chosen.Kind == "income") || (row.AmountMinor > 0 && chosen.Kind == "expense"):
				skip(generated.KindMismatch)
				continue
			}
			if row.CategoryID == chosen.ID {
				results[index] = generated.AiCategoryChoiceResult{TransactionId: choice.TransactionId, Outcome: generated.Unchanged}
				applied.unchanged++
				continue
			}
			results[index] = generated.AiCategoryChoiceResult{TransactionId: choice.TransactionId, Outcome: generated.Categorized}
			applied.categorized++
			applied.byCategory[chosen.Name]++
			applied.transactionIDs = append(applied.transactionIDs, choice.TransactionId.String())
			set.Ids, set.CategoryIds = append(set.Ids, row.ID), append(set.CategoryIds, chosen.ID)
		}
		if len(set.Ids) > 0 {
			if _, err := q.SetAICategories(ctx, set); err != nil {
				return err
			}
		}
		if len(reviewed) > 0 {
			if _, err := q.MarkAIReviewed(ctx, reviewed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return passThrough(err)
	}
	if applied.categorized > 0 {
		h.recordAICategories(c, applied)
		h.budgetChanged()
	}
	return c.JSON(generated.AiCategoryChoicesResult{Categorized: applied.categorized, Unchanged: applied.unchanged, Left: applied.left,
		Skipped: applied.skipped, Results: results})
}

// categoryCounts lists categories with their counts, largest first, then
// by name.
func categoryCounts(counts map[string]int) []map[string]any {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"name": activity.Preview(name, 100), "count": counts[name]})
	}
	return result
}
