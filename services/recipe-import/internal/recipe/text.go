package recipe

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Headings that start the ingredients or the steps, compared in lower case
// without trailing punctuation.
var (
	ingredientHeadings = []string{
		"ingredients", "ingredient", "you need", "you'll need", "you will need", "shopping list",
		"zutaten", "zutatenliste", "du brauchst", "ihr braucht", "einkaufsliste",
		"ингредиенты", "ингредиент", "нам понадобится", "понадобится", "состав", "продукты",
	}
	stepHeadings = []string{
		"steps", "step", "method", "directions", "instructions", "preparation", "how to make it", "how to make",
		"zubereitung", "anleitung", "so geht's", "so gehts", "schritte",
		"приготовление", "способ приготовления", "как приготовить", "шаги",
	}
	numbered   = regexp.MustCompile(`(?i)^(?:(?:step|schritt|шаг)\s*)?\d{1,2}\s*[.):]\s*`)
	servingsRe = regexp.MustCompile(`(?i)(?:(?:serves|für|for|на)\s+(\d{1,3})\b|\b(\d{1,3})\s*(?:servings|serving|portions|portion|portionen|personen|person|people|порци[йия]|персон[ыа]?))`)
	hashtag    = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
)

// stripBullet removes a list marker or decorative symbol at the start of a
// line: "-", "•", "*", "✓", an emoji.
func stripBullet(line string) (string, bool) {
	trimmed := strings.TrimLeftFunc(line, func(r rune) bool {
		return r == '-' || r == '*' || r == '•' || r == '·' || r == '–' || r == '—' || r == '▪' || r == '●' ||
			r == '○' || r == '◦' || r == '>' || r == '+' || unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) ||
			r == '️' || r == '‍' || unicode.IsSpace(r)
	})
	return trimmed, trimmed != strings.TrimSpace(line)
}

// heading reports which section a line heads: "ingredients", "steps", or "".
func heading(line string) string {
	key := strings.ToLower(strings.TrimRightFunc(line, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.Is(unicode.So, r)
	}))
	// "Zutaten für 4 Personen" heads the ingredients too.
	// "Zutaten für 4 Personen:" heads the ingredients too, but a longer line
	// that only starts with the word is text.
	short := len(strings.Fields(key)) <= 5
	for _, word := range ingredientHeadings {
		if key == word || short && strings.HasPrefix(key, word+" ") {
			return "ingredients"
		}
	}
	for _, word := range stepHeadings {
		if key == word || short && strings.HasPrefix(key, word+" ") {
			return "steps"
		}
	}
	return ""
}

// ParseText reads a recipe from pasted text, such as a social post's
// caption, by simple rules: ingredient and step headings, bullet and
// numbered lists, lines that start with an amount, hashtags as tags, and
// "for 4 servings" as the servings. The result is a draft to edit.
func ParseText(text, sourceURL string) Result {
	draft := Draft{SourceURL: strings.TrimSpace(sourceURL)}
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		for _, tag := range hashtag.FindAllString(raw, -1) {
			draft.Tags = addTag(draft.Tags, tag)
		}
		if draft.Servings == 0 {
			if match := servingsRe.FindStringSubmatch(raw); match != nil {
				draft.Servings, _ = strconv.Atoi(firstOf(match[1], match[2]))
			}
		}
		line := strings.TrimSpace(hashtag.ReplaceAllString(raw, ""))
		line, bulleted := stripBullet(line)
		if line == "" {
			continue
		}
		isNumbered := numbered.MatchString(line)
		if next := heading(line); next != "" && !isNumbered {
			section = next
			continue
		}
		if isNumbered {
			line = strings.TrimSpace(numbered.ReplaceAllString(line, ""))
			if line == "" {
				continue
			}
		}
		amount := SplitIngredient(line).Amount != ""
		short := len([]rune(line)) <= 60 && !strings.HasSuffix(line, ".")
		switch {
		case section == "ingredients" && (!isNumbered || short):
			draft.Ingredients = append(draft.Ingredients, SplitIngredient(line))
		case section == "steps":
			draft.Steps = append(draft.Steps, line)
		case section == "ingredients":
			// A long numbered line after the ingredients is a step.
			draft.Steps = append(draft.Steps, line)
			section = "steps"
		case amount && len([]rune(line)) <= 80 || bulleted && short && len(draft.Steps) == 0:
			draft.Ingredients = append(draft.Ingredients, SplitIngredient(line))
		case isNumbered || len(draft.Ingredients) > 0:
			draft.Steps = append(draft.Steps, line)
		case draft.Title == "" && len([]rune(line)) <= 100:
			// Without the emoji a caption's first line often ends with.
			draft.Title = strings.TrimRightFunc(line, func(r rune) bool {
				return unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || r == '\uFE0F' || r == '\u200D' || unicode.IsSpace(r)
			})
		}
	}
	result := Result{Method: Text, Draft: finish(draft)}
	if len(result.Draft.Ingredients) == 0 && len(result.Draft.Steps) == 0 {
		result.Reason = "No ingredients or steps were recognised: add them yourself."
	}
	return result
}
