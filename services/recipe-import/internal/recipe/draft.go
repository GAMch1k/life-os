// Package recipe reads recipe drafts from web pages (schema.org Recipe in
// JSON-LD or microdata, else an OpenGraph preview) and from pasted text. It
// is pure: it fetches nothing and saves nothing.
package recipe

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Method is how a draft was obtained.
type Method string

const (
	// Structured: the page has a schema.org Recipe.
	Structured Method = "structured"
	// Preview: only the page's OpenGraph title, description, and image.
	Preview Method = "preview"
	// Text: read from pasted text.
	Text Method = "text"
	// Failed: nothing could be read.
	Failed Method = "failed"
)

// ReviewTag marks a draft the owner should look over: a preview stub.
const ReviewTag = "to-review"

// Limits, as Food and the contract take them.
const (
	titleLimit       = 200
	nameLimit        = 200
	amountLimit      = 50
	stepLimit        = 2000
	stepsLimit       = 200
	ingredientsLimit = 100
	tagLimit         = 40
	tagsLimit        = 10
	descriptionLimit = 1000
	urlLimit         = 2000
	siteLimit        = 200
)

// Ingredient is one ingredient line: the amount where it could be split
// off, and the rest as the name.
type Ingredient struct {
	Name   string
	Amount string
}

// Draft is a recipe as read, for the owner to edit and save.
type Draft struct {
	Title       string
	Servings    int
	Ingredients []Ingredient
	Steps       []string
	Tags        []string
	SourceURL   string
	ImageURL    string
	Description string
	SiteName    string
}

// Result is a draft and how it was obtained; Reason says why it is not a
// complete recipe.
type Result struct {
	Method Method
	Reason string
	Draft  Draft
}

// social names the sites whose posts carry a recipe only in a caption that
// Life OS cannot read directly.
var social = map[string]string{
	"instagram.com": "Instagram",
	"tiktok.com":    "TikTok",
	"youtube.com":   "YouTube",
	"youtu.be":      "YouTube",
	"facebook.com":  "Facebook",
	"fb.watch":      "Facebook",
	"threads.net":   "Threads",
	"x.com":         "X",
	"twitter.com":   "X",
	"pinterest.com": "Pinterest",
}

// SocialSite is the name of the social site a link is on ("Instagram"), or
// "" for another site.
func SocialSite(u *url.URL) string {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	for domain, name := range social {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return name
		}
	}
	return ""
}

// SiteName is a site's name for people: a social site's name, else its host
// without "www.".
func SiteName(u *url.URL) string {
	if name := SocialSite(u); name != "" {
		return name
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// siteWords are what a page title holds besides the site's name when it
// names only the site or its login wall: "Login • Instagram", "TikTok -
// Make Your Day", "Log into Facebook".
var siteWords = regexp.MustCompile(`\b(log ?in(to)?|sign (in|up)|make your day|page not found|welcome back|home)\b`)

// GenericTitle reports whether a page title names only the site, not the
// post or recipe: the site's name ("Instagram", "YouTube", "chefkoch.de",
// the page's og:site_name), alone or with login words around it. Such a
// title is no title, so a stub is never called "Instagram".
func GenericTitle(title string, link *url.URL, site string) bool {
	names := map[string]bool{}
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		names[name] = true
		for _, word := range titleWords(name) {
			names[word] = true
		}
	}
	host := strings.TrimPrefix(strings.ToLower(link.Hostname()), "www.")
	add(host)
	if labels := strings.Split(host, "."); len(labels) > 1 {
		// "chefkoch" for chefkoch.de.
		add(strings.Join(labels[:len(labels)-1], "."))
	}
	add(site)
	for _, name := range social {
		add(name)
	}
	for _, word := range titleWords(siteWords.ReplaceAllString(strings.ToLower(clean(title)), " ")) {
		if !names[word] {
			return false
		}
	}
	return true
}

// titleWords are a title's words for GenericTitle: runs of letters,
// digits, dots, and hyphens ("chefkoch.de"), without dots at their ends.
func titleWords(text string) []string {
	var words []string
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !hasLetterOrDigit(string(r)) && r != '.' && r != '-' }) {
		if word = strings.Trim(word, ".-"); word != "" {
			words = append(words, word)
		}
	}
	return words
}

// captionReason is why a social post gives only a preview.
func captionReason(site string) string {
	return site + " posts can't be read directly: paste the caption to fill in the ingredients and steps."
}

// FromFetchError is the result for a link whose page could not be fetched:
// a stub with the link, so the owner can still paste a caption.
func FromFetchError(link *url.URL, message string) Result {
	draft := Draft{SourceURL: clip(link.String(), urlLimit), SiteName: SiteName(link), Tags: []string{ReviewTag}}
	if site := SocialSite(link); site != "" {
		return Result{Method: Failed, Reason: strings.TrimSuffix(message, ".") + ". " + captionReason(site), Draft: draft}
	}
	return Result{Method: Failed, Reason: message, Draft: draft}
}

var (
	spaces = regexp.MustCompile(`\s+`)
	tags   = regexp.MustCompile(`<[^>]*>`)
	breaks = regexp.MustCompile(`(?i)<\s*(br|/p|/li|/div|/h[1-6])\s*/?\s*>`)
)

// clean turns text that may hold entities or HTML into one plain line.
func clean(text string) string {
	text = tags.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	return strings.TrimSpace(spaces.ReplaceAllString(text, " "))
}

// lines turns text that may hold HTML into plain lines, breaking at line
// ends and at block-level tags.
func lines(text string) []string {
	text = breaks.ReplaceAllString(text, "\n")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = clean(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// clip shortens text to limit runes, ending with an ellipsis.
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

func hasLetterOrDigit(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// addTag adds a tag as Food keeps it, once: lower case, without "#", at
// most tagLimit characters, and at most tagsLimit tags.
func addTag(list []string, tag string) []string {
	tag = strings.ToLower(clean(strings.TrimLeft(strings.TrimSpace(tag), "#")))
	if tag == "" || !hasLetterOrDigit(tag) || utf8.RuneCountInString(tag) > tagLimit || len(list) >= tagsLimit {
		return list
	}
	for _, existing := range list {
		if existing == tag {
			return list
		}
	}
	return append(list, tag)
}

// finish applies the limits to a draft.
func finish(draft Draft) Draft {
	draft.Title = clip(clean(draft.Title), titleLimit)
	if draft.Servings < 1 || draft.Servings > 100 {
		draft.Servings = 0
	}
	ingredients := draft.Ingredients[:0:0]
	for _, item := range draft.Ingredients {
		item.Name, item.Amount = clip(clean(item.Name), nameLimit), clip(clean(item.Amount), amountLimit)
		if item.Name != "" && len(ingredients) < ingredientsLimit {
			ingredients = append(ingredients, item)
		}
	}
	draft.Ingredients = ingredients
	steps := draft.Steps[:0:0]
	for _, step := range draft.Steps {
		if step = clip(clean(step), stepLimit); step != "" && len(steps) < stepsLimit {
			steps = append(steps, step)
		}
	}
	draft.Steps = steps
	if draft.Tags == nil {
		draft.Tags = []string{}
	}
	draft.SourceURL = clip(draft.SourceURL, urlLimit)
	if utf8.RuneCountInString(draft.ImageURL) > urlLimit {
		draft.ImageURL = ""
	}
	draft.Description = clip(clean(draft.Description), descriptionLimit)
	draft.SiteName = clip(clean(draft.SiteName), siteLimit)
	return draft
}
