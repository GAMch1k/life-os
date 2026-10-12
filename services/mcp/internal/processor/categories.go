package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// The AI categories job (P13-08) runs next to capture processing: the AI
// fills the category gaps Finance's rules leave. Rules and merchant
// category codes categorize first; what stays without a category (booked,
// and nobody chose: the owner's "none" stays none) the job sends to Codex
// in batches, with the owner's categories and their own choices by hand as
// examples, and Codex picks one category per transaction by id, or none
// when unsure, as structured output (CodexCLI.Categories, never with web
// search). The job checks every pick and leaves the ones it keeps with
// finance_set_ai_categories, where Finance applies a pick only while nobody
// else chose: the owner, a rule, or a code always wins. Every transaction of
// a batch is noted as looked at, picked or not, so it is not sent again
// until its text changes.

// categoriesActor is who the job's reads and choices are attributed to.
var categoriesActor = activity.Agent("codex-finance-categories")

const (
	// categoriesBatch is how many transactions one Codex run categorizes.
	categoriesBatch = 40
	// categoriesPoll is how often the job asks Finance whether anything
	// waits (a count: cheap), so a bank sync's new transactions are
	// categorized within minutes.
	categoriesPoll = 5 * time.Minute
	// categoriesDrainGap paces a backlog: the next batch comes this long
	// after the last, leaving Codex to captures in between.
	categoriesDrainGap = 2 * time.Minute
	// categoriesPerDay bounds the batches of one owner-local day; a larger
	// backlog (a full history read) drains over the following days.
	categoriesPerDay = 25
	// minCategoryConfidence is the least confidence a pick needs.
	minCategoryConfidence = 0.75
	// categoriesTick is how often the job checks whether a run is due.
	categoriesTick = 30 * time.Second
	// Limits of what a transaction tells Codex.
	maxPromptCounterparty = 100
	maxPromptDescription  = 200
)

// categoryChooser picks categories; CodexCLI is the real one.
type categoryChooser interface {
	Categories(ctx context.Context, prompt, schema string) (string, error)
}

// CategoriesJob schedules and runs the AI's categories. Its schedule lives
// in memory; what the AI already looked at lives in Finance, so a restart
// sends nothing twice.
type CategoriesJob struct {
	Chooser categoryChooser
	Tools   toolCaller
	// Enabled is LIFE_OS_AI_CATEGORIES; off, the job reads and changes
	// nothing.
	Enabled bool
	// TimeZone is the owner's, for the day the batches are counted in; nil
	// means UTC.
	TimeZone *time.Location
	// Retry schedules a failed run again, as capture processing retries:
	// at the Codex usage reset, else with backoff.
	Retry RetryPolicy
	// Now is the clock; tests replace it.
	Now func() time.Time
	// OnStatus, when set, receives the job's status after every change.
	OnStatus func(CategoriesStatus)
	// Logf, when set, logs a line per batch.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	running bool
	// nextAt is when the job checks next; zero is the next tick.
	nextAt time.Time
	// failures counts the failed runs in a row (for the backoff),
	// badAnswers the unusable answers in a row.
	failures   int
	badAnswers int
	// batchesToday counts the batches of the owner-local day.
	day          string
	batchesToday int
	waiting      *int
	lastCheckAt  *time.Time
	lastRun      *CategoriesRun
}

// CategoriesStatus is the job's part of the processor's /status.
type CategoriesStatus struct {
	Enabled   bool       `json:"enabled"`
	Running   bool       `json:"running"`
	NextRunAt *time.Time `json:"nextRunAt"`
	// Waiting is how many transactions waited for the AI at the last check;
	// null before the first.
	Waiting      *int           `json:"waiting"`
	LastCheckAt  *time.Time     `json:"lastCheckAt"`
	BatchesToday int            `json:"batchesToday"`
	LastRun      *CategoriesRun `json:"lastRun"`
}

// CategoriesRun is what one batch did.
type CategoriesRun struct {
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// Examined transactions went to Codex; Categorized got the AI's
	// category.
	Examined    int `json:"examined"`
	Categorized int `json:"categorized"`
	// Skipped were left without a category: Unsure (no pick, or one below
	// the confidence the job needs), Rejected (an unknown category, or one
	// of the wrong kind), and ChosenMeanwhile (the owner, a rule, or a code
	// chose first).
	Skipped         int `json:"skipped"`
	Unsure          int `json:"unsure"`
	Rejected        int `json:"rejected"`
	ChosenMeanwhile int `json:"chosenMeanwhile"`
	// Outcome: categorized, none (nothing categorized), or failed.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

func (j *CategoriesJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *CategoriesJob) location() *time.Location {
	if j.TimeZone != nil {
		return j.TimeZone
	}
	return time.UTC
}

// Loop checks every categoriesTick whether a run is due and runs it, until
// ctx ends. Switched off, it only reports so. Runs never overlap, and
// Codex's slots keep a batch from planning beside a capture beyond
// LIFE_OS_CODEX_MAX_CONCURRENCY.
func (j *CategoriesJob) Loop(ctx context.Context) {
	j.mu.Lock()
	j.publish()
	j.mu.Unlock()
	if !j.Enabled {
		return
	}
	// The first check comes a tick after startup, when MCP is up.
	ticker := time.NewTicker(categoriesTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.Tick(ctx)
		}
	}
}

// Tick checks Finance once when a check is due and categorizes one batch
// when transactions wait.
func (j *CategoriesJob) Tick(ctx context.Context) {
	allowed, badAnswers, ok := j.take()
	if !ok {
		return
	}
	pass := j.pass(ctx, allowed, badAnswers)
	now := j.now()
	var retryAt time.Time
	if pass.err != nil && ctx.Err() == nil {
		j.mu.Lock()
		previous := j.failures
		j.mu.Unlock()
		retryAt, _ = j.Retry.next(ctx, pass.kind, previous, pass.err.Error())
	}
	if pass.run != nil && j.Logf != nil {
		run := pass.run
		j.Logf("AI categories: %d transactions %s: %d categorized, %d skipped (%d unsure, %d rejected, %d chosen meanwhile); %d waiting%s", run.Examined, run.Outcome, run.Categorized, run.Skipped, run.Unsure, run.Rejected, run.ChosenMeanwhile, pass.waiting, errorSuffix(run.Error))
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	if pass.checked {
		at := now.UTC()
		waiting := pass.waiting
		j.lastCheckAt, j.waiting = &at, &waiting
	}
	if pass.asked {
		j.batchesToday++
	}
	if pass.run != nil {
		j.lastRun = pass.run
	}
	switch {
	case pass.answered:
		j.badAnswers = 0
	case pass.kind == failureModelOutput && pass.err != nil:
		j.badAnswers++
	}
	switch {
	case ctx.Err() != nil:
		// Shutting down: the next start checks again.
	case pass.err != nil:
		j.failures++
		j.nextAt = retryAt
	case pass.limited:
		local := now.In(j.location())
		j.failures, j.nextAt = 0, time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, j.location())
	case pass.run != nil && pass.waiting > 0:
		j.failures, j.nextAt = 0, now.Add(categoriesDrainGap)
	default:
		j.failures, j.nextAt = 0, now.Add(categoriesPoll)
	}
	j.publish()
}

// take claims a due check, if any: whether a batch is still allowed today,
// and how many unusable answers came in a row.
func (j *CategoriesJob) take() (allowed bool, badAnswers int, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	if !j.Enabled || j.running || now.Before(j.nextAt) {
		return false, 0, false
	}
	if day := now.In(j.location()).Format(time.DateOnly); day != j.day {
		j.day, j.batchesToday = day, 0
	}
	j.running = true
	j.publish()
	return j.batchesToday < categoriesPerDay, j.badAnswers, true
}

// Status is the job's status now.
func (j *CategoriesJob) Status() CategoriesStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status()
}

func (j *CategoriesJob) status() CategoriesStatus {
	status := CategoriesStatus{Enabled: j.Enabled, Running: j.running, Waiting: j.waiting, LastCheckAt: j.lastCheckAt, BatchesToday: j.batchesToday, LastRun: j.lastRun}
	if j.Enabled {
		next := j.nextAt
		if now := j.now(); next.Before(now) {
			next = now
		}
		status.NextRunAt = &next
	}
	return status
}

func (j *CategoriesJob) publish() {
	if j.OnStatus != nil {
		j.OnStatus(j.status())
	}
}

// categoriesPass is what one check did.
type categoriesPass struct {
	// checked: Finance said how many wait (waiting, after this batch).
	checked bool
	waiting int
	// limited: transactions wait, but today's batches are used up.
	limited bool
	// asked: Codex was asked; answered: its answer was usable (or the
	// batch was given up on after unusable ones).
	asked, answered bool
	run             *CategoriesRun
	// err, of kind, schedules a retry.
	err  error
	kind failureKind
}

// Finance's answers, as far as the job reads them.
type (
	categoryCandidates struct {
		Total    int                 `json:"total"`
		Items    []categoryCandidate `json:"items"`
		Examples []struct {
			Counterparty string `json:"counterparty"`
			CategoryID   string `json:"categoryId"`
		} `json:"examples"`
	}
	categoryCandidate struct {
		ID          string `json:"id"`
		AccountKind string `json:"accountKind"`
		Amount      struct {
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"amount"`
		BookingDate  string  `json:"bookingDate"`
		Counterparty *string `json:"counterparty"`
		Description  *string `json:"description"`
	}
	financeCategory struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Archived bool   `json:"archived"`
	}
)

// pass reads what waits and, when allowed, categorizes one batch.
func (j *CategoriesJob) pass(ctx context.Context, allowed bool, badAnswers int) categoriesPass {
	started := j.now()
	ctx = activity.WithTrace(ctx, activity.Trace{CorrelationID: "finance-categories-" + runKey(started.UTC().Format(time.RFC3339Nano)), Actor: categoriesActor})
	var pass categoriesPass
	var waiting categoryCandidates
	if err := j.read(ctx, "finance_list_ai_category_candidates", map[string]any{"limit": categoriesBatch}, &waiting); err != nil {
		pass.err, pass.kind = err, failureDependency
		return pass
	}
	pass.checked, pass.waiting = true, waiting.Total
	if waiting.Total == 0 || len(waiting.Items) == 0 {
		return pass
	}
	if !allowed {
		pass.limited = true
		return pass
	}
	var listed struct {
		Items []financeCategory `json:"items"`
	}
	if err := j.read(ctx, "finance_list_categories", map[string]any{}, &listed); err != nil {
		pass.err, pass.kind = err, failureDependency
		return pass
	}
	categories := map[string]financeCategory{}
	for _, category := range listed.Items {
		if !category.Archived && category.ID != "" {
			categories[category.ID] = category
		}
	}
	if len(categories) == 0 {
		// Nothing to choose from yet.
		return pass
	}
	batch := waiting.Items
	if len(batch) > categoriesBatch {
		batch = batch[:categoriesBatch]
	}
	run := &CategoriesRun{StartedAt: started, Examined: len(batch)}
	pass.run = run
	finish := func(outcome string, err error) categoriesPass {
		run.Outcome, run.FinishedAt = outcome, j.now()
		run.Skipped = run.Examined - run.Categorized
		if err != nil {
			run.Error = activity.Preview(err.Error(), 500)
		}
		return pass
	}
	pass.asked = true
	raw, err := j.Chooser.Categories(ctx, categoriesPrompt(listed.Items, waiting, batch), categoriesSchema)
	if err != nil {
		pass.err, pass.kind = err, failureCodex
		run.Skipped = run.Examined
		return finish("failed", err)
	}
	picks, counts, err := checkCategoryChoices(raw, batch, categories)
	if err != nil {
		if badAnswers+1 < maxModelOutputAttempts {
			pass.err, pass.kind = err, failureModelOutput
			return finish("failed", err)
		}
		// Unusable again and again: the batch is left without categories,
		// so the backlog moves on.
		picks, counts = map[string]string{}, categoryCounts{Unsure: len(batch)}
		err = fmt.Errorf("unusable answers %d times, left without a category: %w", maxModelOutputAttempts, err)
	}
	pass.answered = true
	run.Unsure, run.Rejected = counts.Unsure, counts.Rejected
	choices := make([]any, 0, len(batch))
	for _, candidate := range batch {
		choice := map[string]any{"transactionId": candidate.ID, "categoryId": nil}
		if category, ok := picks[candidate.ID]; ok {
			choice["categoryId"] = category
		}
		choices = append(choices, choice)
	}
	result, callErr := j.Tools.Call(ctx, "finance_set_ai_categories", map[string]any{"choices": choices})
	if callErr != nil {
		pass.err, pass.kind = callErr, classifyActionError(callErr)
		return finish("failed", callErr)
	}
	var applied struct {
		Categorized int `json:"categorized"`
		Unchanged   int `json:"unchanged"`
		Results     []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"results"`
	}
	if decodeErr := json.Unmarshal(toolData(result), &applied); decodeErr != nil {
		pass.err, pass.kind = decodeErr, failureDependency
		return finish("failed", decodeErr)
	}
	run.Categorized = applied.Categorized + applied.Unchanged
	for _, item := range applied.Results {
		if item.Outcome != "skipped" {
			continue
		}
		if item.Reason == "chosen" || item.Reason == "not_found" {
			run.ChosenMeanwhile++
		} else {
			run.Rejected++
		}
	}
	pass.waiting = max(waiting.Total-len(batch), 0)
	switch {
	case err != nil:
		return finish("failed", err)
	case run.Categorized > 0:
		return finish("categorized", nil)
	}
	return finish("none", nil)
}

// read calls an MCP read tool and decodes its data into out.
func (j *CategoriesJob) read(ctx context.Context, tool string, arguments map[string]any, out any) error {
	raw, err := j.Tools.Call(ctx, tool, arguments)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if err := json.Unmarshal(toolData(raw), out); err != nil {
		return fmt.Errorf("decode %s: %w", tool, err)
	}
	return nil
}

// categoriesSchema is the JSON Schema of Codex's answer (--output-schema).
// Every field is required, as structured output wants; categoryId may be
// null. The ids and confidences are checked afterwards
// (checkCategoryChoices).
const categoriesSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["choices"],
  "properties": {
    "choices": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["ref", "categoryId", "confidence"],
        "properties": {
          "ref": {"type": "string"},
          "categoryId": {"type": ["string", "null"]},
          "confidence": {"type": "number"}
        }
      }
    }
  }
}`

// categoriesPromptRules are the job's fixed instructions; the run's
// categories, examples, and transactions follow.
const categoriesPromptRules = "You categorize the bank and cash transactions of the owner of Life OS, who lives in Germany. Return only JSON matching the given schema: one choice per transaction below, by its ref. categoryId is the id of one of the owner's categories below, never another; give categoryId null when you are not sure, and never guess. Kinds must fit: money out (direction out) goes to an expense category, money in (direction in) to an income category, never the other way round; money moved between the owner's own accounts (to or from savings, a card, or another of their accounts, such as Umbuchung, Übertrag, Sparplan, or a transfer to themselves) goes to a transfer category, whichever the direction. A refund or a return (money in from a shop) gets null. Judge from the counterparty and the description (German bank texts: Kartenzahlung, Lastschrift, Gutschrift, Dauerauftrag, SEPA, Einkauf bei), the amount, the date, and the account kind; a payment through PayPal, Klarna, or Google or Apple Pay is for the merchant named after it. Learn the owner's taste from examples, their own earlier choices (counterparty and category): follow them for the same or a similar counterparty. A person's name alone says nothing: null, unless an example or the description says what it was. Amazon sells everything: Shopping, unless the description says Prime or a subscription. confidence: from 0 to 1, how sure you are that the owner would choose this category; below 0.75 give categoryId null. "

// promptTransaction is what Codex is told about one transaction.
type promptTransaction struct {
	Ref          string `json:"ref"`
	Date         string `json:"date"`
	Direction    string `json:"direction"`
	Amount       string `json:"amount"`
	AccountKind  string `json:"accountKind"`
	Counterparty string `json:"counterparty,omitempty"`
	Description  string `json:"description,omitempty"`
}

// categoriesPrompt is the prompt of one batch: the rules, the owner's
// categories (id, name, kind), their choices by hand as examples (by the
// category's name), and the transactions with only what is needed, IBANs
// and card numbers removed.
func categoriesPrompt(categories []financeCategory, waiting categoryCandidates, batch []categoryCandidate) string {
	type promptCategory struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	type promptExample struct {
		Counterparty string `json:"counterparty"`
		Category     string `json:"category"`
	}
	owner := struct {
		Categories   []promptCategory    `json:"categories"`
		Examples     []promptExample     `json:"examples"`
		Transactions []promptTransaction `json:"transactions"`
	}{Categories: []promptCategory{}, Examples: []promptExample{}, Transactions: []promptTransaction{}}
	names := map[string]string{}
	for _, category := range categories {
		if category.Archived {
			continue
		}
		names[category.ID] = category.Name
		owner.Categories = append(owner.Categories, promptCategory{ID: category.ID, Name: category.Name, Kind: category.Kind})
	}
	for _, example := range waiting.Examples {
		name, ok := names[example.CategoryID]
		counterparty := promptText(example.Counterparty, maxPromptCounterparty)
		if ok && counterparty != "" {
			owner.Examples = append(owner.Examples, promptExample{Counterparty: counterparty, Category: name})
		}
	}
	for index, candidate := range batch {
		owner.Transactions = append(owner.Transactions, promptTransaction{
			Ref: transactionRef(index), Date: candidate.BookingDate, Direction: direction(candidate.Amount.Amount),
			Amount: promptAmount(candidate.Amount.Amount, candidate.Amount.Currency), AccountKind: candidate.AccountKind,
			Counterparty: promptText(deref(candidate.Counterparty), maxPromptCounterparty), Description: promptText(deref(candidate.Description), maxPromptDescription),
		})
	}
	encoded, _ := json.Marshal(owner)
	return categoriesPromptRules + "Owner's categories, examples, and transactions: " + string(encoded)
}

// transactionRef is a transaction's short name in the prompt: t1, t2, …
func transactionRef(index int) string { return fmt.Sprintf("t%d", index+1) }

func direction(amount int64) string {
	if amount < 0 {
		return "out"
	}
	return "in"
}

// promptAmount is an amount without its sign, such as "23.50 EUR".
func promptAmount(minor int64, currency string) string {
	if minor < 0 {
		minor = -minor
	}
	return fmt.Sprintf("%d.%02d %s", minor/100, minor%100, currency)
}

// promptText is a text as Codex may see it: IBANs, card numbers, and other
// long numbers removed, whitespace collapsed, and at most limit characters.
func promptText(text string, limit int) string {
	text = strings.Join(strings.Fields(redactFinanceText(text)), " ")
	if utf8.RuneCountInString(text) > limit {
		text = string([]rune(text)[:limit])
	}
	return text
}

var (
	// ibanLike: two letters, two check digits, then the account in groups
	// of letters or digits, maybe spaced; kept as an IBAN only with at least
	// ten digits, so words never are.
	ibanLike = regexp.MustCompile(`(?i)\b[A-Z]{2}[0-9]{2}(?:[ -]?[A-Z0-9]{4}){2,7}(?:[ -]?[A-Z0-9]{1,4})?\b`)
	// cardLike: 12 to 19 digits, maybe grouped by spaces or dashes, or
	// partly masked (XXXX XXXX XXXX 1234, 5168 41** **** 1234).
	cardLike = regexp.MustCompile(`[0-9Xx*•](?:[ -]?[0-9Xx*•]){11,24}`)
	// longNumber: what is left of references, mandates, and account
	// numbers; nothing to categorize by.
	longNumber = regexp.MustCompile(`[0-9]{8,}`)
)

// redactFinanceText removes IBAN-like strings, anything that looks like a
// card number, and other long numbers from a bank text before it reaches
// Codex.
func redactFinanceText(text string) string {
	text = ibanLike.ReplaceAllStringFunc(text, func(match string) string {
		// Words after the account stay ("DE89 3704 … 00 REWE" keeps REWE).
		tokens := strings.Fields(strings.ReplaceAll(match, "-", " "))
		kept := []string{}
		for len(tokens) > 1 && !strings.ContainsAny(tokens[len(tokens)-1], "0123456789") {
			kept = append([]string{tokens[len(tokens)-1]}, kept...)
			tokens = tokens[:len(tokens)-1]
		}
		if digits(strings.Join(tokens, "")) < 10 {
			return match
		}
		return strings.Join(append([]string{"[IBAN]"}, kept...), " ")
	})
	text = cardLike.ReplaceAllStringFunc(text, func(match string) string {
		trimmed := strings.TrimRight(match, " -")
		marks := utf8.RuneCountInString(strings.NewReplacer(" ", "", "-", "").Replace(trimmed))
		switch count := digits(trimmed); {
		case count < 4:
			return match
		case marks > 19:
			return "[number]" + match[len(trimmed):]
		}
		return "[card]" + match[len(trimmed):]
	})
	return longNumber.ReplaceAllString(text, "[number]")
}

func digits(text string) int {
	count := 0
	for _, r := range text {
		if r >= '0' && r <= '9' {
			count++
		}
	}
	return count
}

// categoryCounts are the picks the job did not keep.
type categoryCounts struct {
	// Unsure: no pick, or one below minCategoryConfidence.
	Unsure int
	// Rejected: an unknown category, or one of the wrong kind.
	Rejected int
}

// checkCategoryChoices decodes Codex's answer and keeps the picks that pass
// every check: a transaction of the batch (the first choice for it), one of
// the owner's active categories, at least minCategoryConfidence, and a
// category whose kind fits the money (out to expense or transfer, in to
// income or transfer). It returns the kept picks by transaction id and
// counts the rest; a transaction without a choice is unsure. An answer
// that is not the schema's JSON is an error.
func checkCategoryChoices(raw string, batch []categoryCandidate, categories map[string]financeCategory) (map[string]string, categoryCounts, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var answer struct {
		Choices *[]struct {
			Ref        string   `json:"ref"`
			CategoryID *string  `json:"categoryId"`
			Confidence *float64 `json:"confidence"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(clean), &answer); err != nil {
		return nil, categoryCounts{}, fmt.Errorf("decode categories: %w", err)
	}
	if answer.Choices == nil {
		return nil, categoryCounts{}, errors.New("decode categories: no choices")
	}
	byRef := make(map[string]categoryCandidate, len(batch))
	for index, candidate := range batch {
		byRef[transactionRef(index)] = candidate
	}
	picks := map[string]string{}
	decided := map[string]bool{}
	var counts categoryCounts
	for _, choice := range *answer.Choices {
		candidate, ok := byRef[strings.TrimSpace(choice.Ref)]
		if !ok || decided[candidate.ID] {
			continue
		}
		decided[candidate.ID] = true
		id := strings.TrimSpace(deref(choice.CategoryID))
		confidence := 0.0
		if choice.Confidence != nil && !math.IsNaN(*choice.Confidence) {
			confidence = *choice.Confidence
		}
		if id == "" || confidence < minCategoryConfidence || confidence > 1 {
			counts.Unsure++
			continue
		}
		category, known := categories[id]
		if !known || category.Archived || !kindFits(candidate.Amount.Amount, category.Kind) {
			counts.Rejected++
			continue
		}
		picks[candidate.ID] = category.ID
	}
	counts.Unsure += len(batch) - len(decided)
	return picks, counts, nil
}

// kindFits reports whether money of this sign may go to a category of this
// kind: out to expense or transfer, in to income or transfer.
func kindFits(amount int64, kind string) bool {
	switch kind {
	case "transfer":
		return true
	case "expense":
		return amount < 0
	case "income":
		return amount > 0
	}
	return false
}
