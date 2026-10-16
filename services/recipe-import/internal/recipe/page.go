package recipe

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// page is what the parser takes from a page's HTML: its JSON-LD blocks,
// its meta tags, its <title>, and its root for microdata.
type page struct {
	root   *html.Node
	jsonLD []string
	meta   map[string]string
	title  string
}

func readPage(body []byte) (page, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return page{}, err
	}
	p := page{root: root, meta: map[string]string{}}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.DataAtom {
			case atom.Script:
				if strings.Contains(strings.ToLower(attr(node, "type")), "ld+json") {
					p.jsonLD = append(p.jsonLD, textContent(node))
				}
			case atom.Meta:
				key := strings.ToLower(attr(node, "property"))
				if key == "" {
					key = strings.ToLower(attr(node, "name"))
				}
				if key != "" && attr(node, "content") != "" {
					if _, seen := p.meta[key]; !seen {
						p.meta[key] = attr(node, "content")
					}
				}
			case atom.Title:
				if p.title == "" {
					p.title = textContent(node)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return p, nil
}

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func textContent(node *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return b.String()
}

// Parse reads a recipe draft from a page fetched from link: a schema.org
// Recipe in JSON-LD, else in microdata (structured), else the page's
// OpenGraph title, description, and image (a preview tagged to-review),
// else nothing (failed).
func Parse(link *url.URL, body []byte) Result {
	p, err := readPage(body)
	site := SiteName(link)
	if name := strings.TrimSpace(p.meta["og:site_name"]); name != "" && SocialSite(link) == "" {
		site = name
	}
	base := Draft{SourceURL: link.String(), SiteName: site}
	if err == nil {
		for _, block := range p.jsonLD {
			if found, ok := fromJSONLD(block); ok {
				found.SourceURL, found.SiteName = base.SourceURL, base.SiteName
				if found.ImageURL == "" {
					found.ImageURL = p.meta["og:image"]
				}
				if found.Description == "" {
					found.Description = p.description()
				}
				found.ImageURL = absolute(link, found.ImageURL)
				return Result{Method: Structured, Draft: finish(found)}
			}
		}
		if found, ok := fromMicrodata(p.root); ok {
			found.SourceURL, found.SiteName = base.SourceURL, base.SiteName
			if found.ImageURL == "" {
				found.ImageURL = p.meta["og:image"]
			}
			found.ImageURL = absolute(link, found.ImageURL)
			return Result{Method: Structured, Draft: finish(found)}
		}
	}
	stub := base
	stub.Title = firstOf(p.meta["og:title"], p.meta["twitter:title"], p.title)
	if GenericTitle(stub.Title, link, site) {
		// A login wall ("Instagram", "Login • Instagram") names the site,
		// not the post: no title at all.
		stub.Title = ""
	}
	stub.Description = p.description()
	stub.ImageURL = absolute(link, firstOf(p.meta["og:image"], p.meta["twitter:image"]))
	stub.Tags = []string{ReviewTag}
	social := SocialSite(link)
	if social == "Instagram" {
		stub.Description = instagramCaption(stub.Description)
	}
	if clean(stub.Title) == "" {
		reason := "The page has no recipe and no title that could be read."
		if social != "" {
			reason = captionReason(social)
		}
		return Result{Method: Failed, Reason: reason, Draft: finish(stub)}
	}
	reason := "This page has no recipe data, so only its title, description, and image were read."
	if social != "" {
		reason = captionReason(social)
	}
	return Result{Method: Preview, Reason: reason, Draft: finish(stub)}
}

// instagramPrefix is how Instagram's preview text starts before the caption:
// `1,234 likes, 56 comments - anna.cooks on October 1, 2026: "…"`.
var instagramPrefix = regexp.MustCompile(`^[\d.,]+[KkMm]? likes?, [\d.,]+[KkMm]? comments? - .+? on [^:]+: ["“](.*?)["”]?\.?$`)

// instagramCaption is the caption in an Instagram preview text, or the text
// as it is.
func instagramCaption(text string) string {
	text = clean(text)
	if match := instagramPrefix.FindStringSubmatch(text); match != nil {
		return match[1]
	}
	return text
}

func (p page) description() string {
	return firstOf(p.meta["og:description"], p.meta["description"], p.meta["twitter:description"])
}

func firstOf(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// absolute resolves an image link against the page; "" when it is not an
// http(s) link.
func absolute(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	resolved, err := base.Parse(raw)
	if err != nil || (resolved.Scheme != "http" && resolved.Scheme != "https") {
		return ""
	}
	return resolved.String()
}

// ---- JSON-LD ----------------------------------------------------------------

var cdata = strings.NewReplacer("<![CDATA[", "", "]]>", "", "<!--", "", "-->", "")

// fromJSONLD reads the first schema.org Recipe in a JSON-LD block: the
// block itself, an array of objects, or an @graph (also nested in
// mainEntity).
func fromJSONLD(block string) (Draft, bool) {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(cdata.Replace(block))), &value); err != nil {
		return Draft{}, false
	}
	node := findRecipe(value, 0)
	if node == nil {
		return Draft{}, false
	}
	draft := Draft{
		Title:       firstOf(stringOf(node["name"]), stringOf(node["headline"])),
		Servings:    servingsOf(node["recipeYield"]),
		Description: stringOf(node["description"]),
		ImageURL:    imageOf(node["image"]),
	}
	if clean(draft.Title) == "" {
		return Draft{}, false
	}
	ingredients := node["recipeIngredient"]
	if ingredients == nil {
		ingredients = node["ingredients"]
	}
	for _, line := range stringsOf(ingredients) {
		// "For the dough:" lines head a group; they are no ingredient.
		if line = clean(line); line != "" && !strings.HasSuffix(line, ":") {
			draft.Ingredients = append(draft.Ingredients, SplitIngredient(line))
		}
	}
	draft.Steps = instructions(node["recipeInstructions"], 0)
	for _, key := range []string{"recipeCategory", "recipeCuisine", "keywords"} {
		for _, value := range stringsOf(node[key]) {
			for _, tag := range strings.Split(value, ",") {
				draft.Tags = addTag(draft.Tags, tag)
			}
		}
	}
	return draft, true
}

func isRecipe(node map[string]any) bool {
	for _, kind := range stringsOf(node["@type"]) {
		kind = strings.ToLower(kind)
		if kind == "recipe" || strings.HasSuffix(kind, "/recipe") || strings.HasSuffix(kind, ":recipe") {
			return true
		}
	}
	return false
}

func findRecipe(value any, depth int) map[string]any {
	if depth > 6 {
		return nil
	}
	switch node := value.(type) {
	case []any:
		for _, item := range node {
			if found := findRecipe(item, depth+1); found != nil {
				return found
			}
		}
	case map[string]any:
		if isRecipe(node) {
			return node
		}
		for _, key := range []string{"@graph", "mainEntity", "mainEntityOfPage", "itemListElement", "item"} {
			if found := findRecipe(node[key], depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

// stringOf is a JSON-LD value as text: a string, a number, or the first of
// a list.
func stringOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		for _, item := range v {
			if text := stringOf(item); text != "" {
				return text
			}
		}
	case map[string]any:
		return firstOf(stringOf(v["name"]), stringOf(v["text"]), stringOf(v["@value"]))
	}
	return ""
}

// stringsOf is a JSON-LD value as a list of texts.
func stringsOf(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, item := range v {
			if text := stringOf(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	if text := stringOf(value); text != "" {
		return []string{text}
	}
	return nil
}

var firstNumber = regexp.MustCompile(`\d+`)

// servingsOf reads recipeYield: 4, "4", "4 servings", "4 Portionen", or a
// list of those.
func servingsOf(value any) int {
	for _, text := range stringsOf(value) {
		if match := firstNumber.FindString(text); match != "" {
			if n, err := strconv.Atoi(match); err == nil && n >= 1 && n <= 100 {
				return n
			}
		}
	}
	return 0
}

func imageOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if image := imageOf(item); image != "" {
				return image
			}
		}
	case map[string]any:
		return firstOf(stringOf(v["url"]), stringOf(v["contentUrl"]))
	}
	return ""
}

// instructions flattens recipeInstructions: text (with lines or HTML), a
// list of texts, HowToStep objects, and HowToSection or ItemList objects
// holding steps. Sections' names are left out: the steps follow in order.
func instructions(value any, depth int) []string {
	if depth > 6 {
		return nil
	}
	switch v := value.(type) {
	case string:
		return lines(v)
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, instructions(item, depth+1)...)
		}
		return out
	case map[string]any:
		if items, ok := v["itemListElement"]; ok {
			return instructions(items, depth+1)
		}
		if text := firstOf(stringOf(v["text"]), stringOf(v["description"]), stringOf(v["name"])); text != "" {
			return lines(text)
		}
	}
	return nil
}

// ---- Microdata --------------------------------------------------------------

// fromMicrodata reads the first element marked itemtype schema.org Recipe.
func fromMicrodata(root *html.Node) (Draft, bool) {
	scope := findScope(root)
	if scope == nil {
		return Draft{}, false
	}
	var draft Draft
	var yield string
	var visit func(node *html.Node, top bool)
	visit = func(node *html.Node, top bool) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			prop := strings.ToLower(attr(child, "itemprop"))
			value := strings.TrimSpace(itemValue(child))
			switch prop {
			case "name":
				if top && draft.Title == "" {
					draft.Title = value
				}
			case "recipeyield":
				yield = value
			case "recipeingredient", "ingredients":
				if line := clean(value); line != "" {
					draft.Ingredients = append(draft.Ingredients, SplitIngredient(line))
				}
				continue
			case "recipeinstructions":
				draft.Steps = append(draft.Steps, blockLines(child)...)
				continue
			case "image":
				if draft.ImageURL == "" {
					draft.ImageURL = value
				}
			case "description":
				if top && draft.Description == "" {
					draft.Description = value
				}
			case "recipecategory", "recipecuisine", "keywords":
				for _, tag := range strings.Split(value, ",") {
					draft.Tags = addTag(draft.Tags, tag)
				}
			}
			// A nested item (an author, a rating) has names of its own.
			visit(child, top && !hasAttr(child, "itemscope"))
		}
	}
	visit(scope, true)
	draft.Servings = servingsOf(yield)
	if clean(draft.Title) == "" {
		return Draft{}, false
	}
	return draft, true
}

func hasAttr(node *html.Node, name string) bool {
	for _, a := range node.Attr {
		if strings.EqualFold(a.Key, name) {
			return true
		}
	}
	return false
}

func findScope(node *html.Node) *html.Node {
	if node.Type == html.ElementNode && hasAttr(node, "itemscope") {
		kind := strings.ToLower(attr(node, "itemtype"))
		if strings.HasSuffix(strings.TrimRight(kind, "/"), "schema.org/recipe") {
			return node
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findScope(child); found != nil {
			return found
		}
	}
	return nil
}

// itemValue is a microdata property's value: its content, src, href, or
// datetime attribute, else its text.
func itemValue(node *html.Node) string {
	for _, name := range []string{"content", "src", "href", "datetime"} {
		if value := attr(node, name); value != "" {
			return value
		}
	}
	return textContent(node)
}

// block-level elements break text into lines.
var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Li: true, atom.Div: true, atom.Br: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.Ol: true, atom.Ul: true, atom.Section: true,
}

// blockLines is an element's text as lines, broken at block-level elements.
func blockLines(node *html.Node) []string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && blocks[n.DataAtom] {
			b.WriteString("\n")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && blocks[n.DataAtom] {
			b.WriteString("\n")
		}
	}
	walk(node)
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		if line = clean(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
