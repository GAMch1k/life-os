package domain

import "strings"

// Cooking methods, in the order Food lists them.
var Methods = []string{"air_fryer", "oven", "stovetop", "slow_cooker", "grill", "microwave", "no_cook"}

// ValidMethod reports whether method is one of Methods.
func ValidMethod(method string) bool {
	for _, known := range Methods {
		if method == known {
			return true
		}
	}
	return false
}

// MethodLabel is a method in words: "Air fryer".
func MethodLabel(method string) string {
	switch method {
	case "air_fryer":
		return "Air fryer"
	case "oven":
		return "Oven"
	case "stovetop":
		return "Stovetop"
	case "slow_cooker":
		return "Slow cooker"
	case "grill":
		return "Grill"
	case "microwave":
		return "Microwave"
	case "no_cook":
		return "No cooking"
	}
	return method
}

// methodRule finds a method by its keywords, written normalised. A keyword
// matches whole words, in order; a trailing "*" lets its last word start a
// longer one ("аэрогрил*" finds "аэрогриле", "roast*" finds "roasted").
type methodRule struct {
	method   string
	keywords []string
	// reach is how many words around a match also belong to it: a device's
	// rule takes the verb next to it ("Bake in the air fryer", "Запекать в
	// аэрогриле" is the air fryer, not the oven).
	reach int
}

// methodRules are checked in this order, and the words a rule matched are
// not looked at again: "air fryer" and "аэрогриль" are the air fryer, not
// also frying or the grill, and "ohne kochen" is no cooking, not the stove.
var methodRules = []methodRule{
	{"air_fryer", []string{"air fryer*", "airfryer*", "air fry*", "air fried", "airfry*", "heißluftfritteuse*", "heissluftfritteuse*", "heißluft fritteuse*",
		"аэрогрил*", "аэрофритюрниц*"}, 3},
	{"slow_cooker", []string{"slow cooker*", "slowcooker*", "crock pot*", "crockpot*", "schongarer*", "slow cook*", "мультиварк*", "медленноварк*"}, 3},
	{"microwave", []string{"microwave*", "mikrowelle*", "микроволнов*", "свч"}, 3},
	{"no_cook", []string{"no cook", "no bake", "without cooking", "ohne kochen", "ohne backen", "без варки", "без выпечки", "без готовки", "без термообработки"}, 0},
	{"grill", []string{"grill*", "bbq", "barbecue*", "gegrillt*", "гриль*", "мангал*", "шашлык*"}, 0},
	{"oven", []string{"oven*", "bake", "baked", "bakes", "baking", "roast*", "casserole*", "backofen*", "ofen*", "backen", "gebacken*", "überbacken*", "auflauf*",
		"духовк*", "запек*", "запеч*", "выпек*"}, 0},
	{"stovetop", []string{"pan", "pans", "frying pan*", "skillet*", "wok*", "saute*", "sauté*", "simmer*", "boil*", "fry", "fried", "frying", "stir fry*",
		"pfanne*", "braten", "anbraten", "kochen", "köcheln*", "topf*", "dünsten", "сковород*", "обжар*", "жарить", "обжарить", "варить", "свари*", "кастрюл*", "отвар*"}, 0},
}

// InferMethods works out a recipe's cooking methods from its title, tags,
// and steps (in English, German, or Russian): "Air fryer chicken wings",
// "Hähnchen aus der Heißluftfritteuse", and "Курица в аэрогриле" are the air
// fryer; "im Backofen backen" the oven; "in der Pfanne anbraten" the stove.
// The result follows Methods' order; none found is empty.
func InferMethods(title string, tags []string, steps string) []string {
	text := []string{title}
	text = append(text, tags...)
	text = append(text, steps)
	tokens := words(Normalize(strings.Join(text, "\n")))
	found := map[string]bool{}
	for _, rule := range methodRules {
		for _, keyword := range rule.keywords {
			if consume(tokens, keyword, rule.reach) {
				found[rule.method] = true
			}
		}
	}
	out := []string{}
	for _, method := range Methods {
		if found[method] {
			out = append(out, method)
		}
	}
	return out
}

// consume marks every occurrence of keyword in tokens, and reach words on
// either side of it, as used (empties them) and reports whether there was
// one.
func consume(tokens []string, keyword string, reach int) bool {
	prefix := strings.HasSuffix(keyword, "*")
	parts := words(strings.TrimSuffix(keyword, "*"))
	if len(parts) == 0 {
		return false
	}
	matched := false
	for start := 0; start+len(parts) <= len(tokens); start++ {
		ok := true
		for index, part := range parts {
			token := tokens[start+index]
			last := index == len(parts)-1
			if token == "" || !(token == part || last && prefix && strings.HasPrefix(token, part)) {
				ok = false
				break
			}
		}
		if ok {
			for index := max(0, start-reach); index < min(len(tokens), start+len(parts)+reach); index++ {
				tokens[index] = ""
			}
			matched = true
		}
	}
	return matched
}
