package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Ranking weights: what each reason adds to a recipe's score. Coverage is
// the most of it; what is about to expire and the preferred method come
// next; habits add a little, capped so the same meal is not suggested over
// and over; a meal planned in the last days (or the next ones) loses some.
const (
	CoveragePoints   = 40 // for having every ingredient
	ExpiringPoints   = 12 // per expiring item used, at most ExpiringCounted
	ExpiringCounted  = 3
	ExpiringDays     = 3 // expires today through today + ExpiringDays
	MethodPoints     = 10
	BoughtPoints     = 8 // for having bought every ingredient lately
	SlotPoints       = 5
	OtherSlotPenalty = -10
	RecentPenalty    = -10
	RecentDays       = 3 // planned today ± RecentDays
	HistoryDays      = 60
)

// plannedPoints rewards a meal planned before, with diminishing returns:
// once 4, twice 7, three times or more 9.
var plannedPoints = []int{0, 4, 7, 9}

// Reason kinds.
const (
	ReasonCoverage = "coverage"
	ReasonExpiring = "expiring"
	ReasonMethod   = "method"
	ReasonPlanned  = "habit_planned"
	ReasonBought   = "habit_bought"
	ReasonSlot     = "slot"
	ReasonRecent   = "recent"
)

// slotWords are tags that name a meal slot, in English, German, and Russian.
var slotWords = map[string][]string{
	"breakfast": {"breakfast", "frühstück", "завтрак"},
	"lunch":     {"lunch", "mittagessen", "mittag", "обед"},
	"dinner":    {"dinner", "abendessen", "abendbrot", "ужин"},
}

// StockedItem is an in-stock pantry item.
type StockedItem struct {
	ID, Name, Normalized string
	// ExpiresOn is a plain date (UTC midnight); zero when it has none.
	ExpiresOn time.Time
}

// RecipeIngredient is one ingredient of a recipe.
type RecipeIngredient struct {
	Name, Normalized string
	Amount           *string
}

// PlannedMeal is a day the recipe was (or is) planned for.
type PlannedMeal struct {
	Day  time.Time
	Slot string
}

// RecipeFacts is what the ranking knows of a recipe.
type RecipeFacts struct {
	Title, NormalizedTitle string
	Ingredients            []RecipeIngredient
	Methods                []string
	Tags                   []string
	// Planned are the days it was planned, from HistoryDays ago through
	// RecentDays ahead.
	Planned []PlannedMeal
}

// Household is what the ranking knows of the household.
type Household struct {
	// Today is the owner's today (a plain date, UTC midnight).
	Today time.Time
	// Slot is the meal being chosen for, or empty.
	Slot    string
	Stocked []StockedItem
	// Bought are the normalised names bought or put away in the last
	// HistoryDays.
	Bought []string
	// Preferred are the preferred cooking methods, the most preferred first.
	Preferred []string
	// Dislikes are disliked ingredients, normalised.
	Dislikes []string
}

// Reason is one part of a score, in words.
type Reason struct {
	Kind   string
	Points int
	Text   string
}

// Use is an ingredient the household has: the stocked item that covers it.
type Use struct {
	Ingredient RecipeIngredient
	Item       StockedItem
	// Expiring is set when the item expires within ExpiringDays.
	Expiring bool
	// Days until it expires (0 today), when Expiring.
	Days int
}

// Ranked is a recipe's score and why.
type Ranked struct {
	Score   int
	Reasons []Reason
	Have    []Use
	Missing []RecipeIngredient
	// Basics are the ingredients assumed at home (see Basic).
	Basics []RecipeIngredient
	// Disliked names the disliked ingredient that rules it out; empty
	// otherwise.
	Disliked string
}

// Rank scores a recipe for a household. A recipe with a disliked
// ingredient (or a disliked word in its title) is ruled out (Disliked set,
// no score).
func Rank(recipe RecipeFacts, home Household) Ranked {
	var out Ranked
	for _, dislike := range home.Dislikes {
		if NamesMatch(dislike, recipe.NormalizedTitle) {
			out.Disliked = dislike
			return out
		}
		for _, ingredient := range recipe.Ingredients {
			if NamesMatch(dislike, ingredient.Normalized) {
				out.Disliked = ingredient.Name
				return out
			}
		}
	}

	// Coverage, and what of it expires soon.
	counted := 0
	for _, ingredient := range recipe.Ingredients {
		if Basic(ingredient.Normalized) {
			out.Basics = append(out.Basics, ingredient)
			continue
		}
		counted++
		if use, ok := covering(ingredient, home); ok {
			out.Have = append(out.Have, use)
		} else {
			out.Missing = append(out.Missing, ingredient)
		}
	}
	if counted > 0 {
		points := int(math.Round(CoveragePoints * float64(len(out.Have)) / float64(counted)))
		text := fmt.Sprintf("You have %d of its %d ingredients", len(out.Have), counted)
		if len(out.Have) == counted {
			text = fmt.Sprintf("You have all %d of its ingredients", counted)
			if counted == 1 {
				text = "You have its only ingredient"
			}
		}
		out.add(ReasonCoverage, points, text)
	}
	var expiring []string
	seen := map[string]bool{}
	for _, use := range out.Have {
		if use.Expiring && !seen[use.Item.ID] && len(expiring) < ExpiringCounted {
			seen[use.Item.ID] = true
			expiring = append(expiring, use.Item.Name+" expires "+inDays(use.Days))
		}
	}
	if len(expiring) > 0 {
		out.add(ReasonExpiring, ExpiringPoints*len(expiring), strings.Join(expiring, ", "))
	}

	// The preferred cooking method.
	for _, preferred := range home.Preferred {
		if contains(recipe.Methods, preferred) {
			out.add(ReasonMethod, MethodPoints, MethodLabel(preferred)+", your preferred way")
			break
		}
	}

	// Habits: planned before, and its ingredients bought lately.
	past, recent, inSlot := 0, -1000, false
	for _, meal := range recipe.Planned {
		days := int(math.Round(meal.Day.Sub(home.Today).Hours() / 24))
		if days < 0 && days >= -HistoryDays {
			past++
			if home.Slot != "" && meal.Slot == home.Slot {
				inSlot = true
			}
		}
		if days >= -RecentDays && days <= RecentDays && (recent == -1000 || abs(days) < abs(recent)) {
			recent = days
		}
	}
	if past > 0 {
		times := "once"
		if past == 2 {
			times = "twice"
		} else if past > 2 {
			times = fmt.Sprintf("%d times", past)
		}
		out.add(ReasonPlanned, plannedPoints[min(past, len(plannedPoints)-1)], fmt.Sprintf("You had it %s in the last %d days", times, HistoryDays))
	}
	if counted > 0 && len(home.Bought) > 0 {
		bought := 0
		for _, ingredient := range recipe.Ingredients {
			if Basic(ingredient.Normalized) {
				continue
			}
			for _, name := range home.Bought {
				if Covers(name, ingredient.Normalized) {
					bought++
					break
				}
			}
		}
		if bought > 0 {
			text := fmt.Sprintf("You bought %d of its ingredients lately", bought)
			if bought == 1 {
				text = "You bought one of its ingredients lately"
			}
			out.add(ReasonBought, int(math.Round(BoughtPoints*float64(bought)/float64(counted))), text)
		}
	}
	if recent != -1000 {
		out.add(ReasonRecent, RecentPenalty, plannedWhen(recent))
	}

	// The meal being chosen for.
	if home.Slot != "" {
		tagged, other := slotTags(recipe.Tags)
		switch {
		case tagged[home.Slot] || inSlot:
			out.add(ReasonSlot, SlotPoints, "Fits "+home.Slot)
		case len(other) > 0:
			out.add(ReasonSlot, OtherSlotPenalty, "Usually "+other[0])
		}
	}
	return out
}

func (r *Ranked) add(kind string, points int, text string) {
	r.Score += points
	r.Reasons = append(r.Reasons, Reason{Kind: kind, Points: points, Text: text})
}

// covering finds the stocked item that covers an ingredient: an item named
// exactly like it first, else the one Covers matches that expires soonest.
func covering(ingredient RecipeIngredient, home Household) (Use, bool) {
	var best *StockedItem
	for index := range home.Stocked {
		item := &home.Stocked[index]
		if item.Normalized == ingredient.Normalized {
			if best == nil || best.Normalized != ingredient.Normalized || sooner(item, best) {
				best = item
			}
			continue
		}
		if (best == nil || best.Normalized != ingredient.Normalized && sooner(item, best)) && Covers(item.Normalized, ingredient.Normalized) {
			best = item
		}
	}
	if best == nil {
		return Use{}, false
	}
	use := Use{Ingredient: ingredient, Item: *best}
	if !best.ExpiresOn.IsZero() {
		days := int(math.Round(best.ExpiresOn.Sub(home.Today).Hours() / 24))
		if days >= 0 && days <= ExpiringDays {
			use.Expiring, use.Days = true, days
		}
	}
	return use, true
}

// sooner reports whether a expires before b (an item without a date never
// does).
func sooner(a, b *StockedItem) bool {
	switch {
	case a.ExpiresOn.IsZero():
		return false
	case b.ExpiresOn.IsZero():
		return true
	}
	return a.ExpiresOn.Before(b.ExpiresOn)
}

func slotTags(tags []string) (map[string]bool, []string) {
	tagged := map[string]bool{}
	var slots []string
	for _, slot := range []string{"breakfast", "lunch", "dinner"} {
		for _, tag := range tags {
			if contains(slotWords[slot], tag) {
				tagged[slot] = true
				slots = append(slots, slot)
				break
			}
		}
	}
	return tagged, slots
}

// inDays says when something expires: "today", "tomorrow", "in 2 days".
func inDays(days int) string {
	switch days {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", days)
}

// plannedWhen says when a meal was or is planned, days from today.
func plannedWhen(days int) string {
	switch {
	case days == 0:
		return "Planned for today"
	case days == -1:
		return "Planned yesterday"
	case days < 0:
		return fmt.Sprintf("Planned %d days ago", -days)
	case days == 1:
		return "Already planned for tomorrow"
	}
	return fmt.Sprintf("Already planned in %d days", days)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
