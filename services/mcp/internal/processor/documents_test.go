package processor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

// documentRefs name the test's photos by their media references.
var documentRefs = map[string]string{
	"inbox-media:" + strings.Repeat("1", 64): "letter-finanzamt",
	"inbox-media:" + strings.Repeat("2", 64): "receipt-cash",
	"inbox-media:" + strings.Repeat("3", 64): "receipt-card",
	"inbox-media:" + strings.Repeat("4", 64): "letter-aok",
	"inbox-media:" + strings.Repeat("5", 64): "letter-termin",
}

func documentRef(name string) string {
	for reference, document := range documentRefs {
		if document == name {
			return reference
		}
	}
	panic(name)
}

// documentMedia hands the planner each photo with its document's name as
// its bytes.
type documentMedia struct{}

func (documentMedia) Fetch(_ context.Context, references []string) (CaptureImages, error) {
	var images CaptureImages
	for _, reference := range references {
		images.Images = append(images.Images, Image{ContentType: "image/png", Data: []byte(documentRefs[reference])})
	}
	return images, nil
}

// documentPlanner plans the photos the way the plan prompt asks, from the
// images it is given (the text is only the placeholder "Photo").
type documentPlanner struct{}

func (documentPlanner) Plan(ctx context.Context, input string) (string, error) {
	images := imagesFrom(ctx)
	if len(images.Images) != 1 || !strings.Contains(input, `"content":"Photo"`) {
		return `{"actions":[],"ambiguous":true,"confidence":0,"reason":"no photo"}`, nil
	}
	switch string(images.Images[0].Data) {
	case "letter-finanzamt":
		return `{"actions":[{"tool":"task_create","arguments":{"title":"Finanzamt Darmstadt: send documents","dueAt":"2026-11-30T00:00:00+01:00","notes":"Documents for the income tax 2025 (Werbungskosten).\nSteuernummer 007/123/45678.\nDeadline printed in the letter of 02.10.2026."}}],"confidence":0.95}`, nil
	case "receipt-cash":
		return `{"actions":[{"tool":"finance_create_manual_transaction","arguments":{"amount":{"amount":-2347,"currency":"EUR"},"counterparty":"REWE","occurredOn":"2026-10-10"}}],"confidence":0.95}`, nil
	case "receipt-card":
		return `{"actions":[],"ambiguous":true,"confidence":0.9,"reason":"Card and bank payments arrive from the bank connection."}`, nil
	case "letter-termin":
		return `{"actions":[{"tool":"calendar_create","arguments":{"title":"Bürgeramt appointment","location":"Bürgeramt Darmstadt, Luisenplatz 5","allDay":false,"startAt":"2026-10-20T09:30:00+02:00"}},{"tool":"task_create","arguments":{"title":"Bürgeramt: send the form","dueAt":"2026-10-16T00:00:00+02:00","notes":"Form for the appointment.\nAktenzeichen 33-4711."}}],"confidence":0.9}`, nil
	default:
		return `{"actions":[],"ambiguous":true,"confidence":0.9,"reason":"A letter from AOK with no deadline: what should I do with it?"}`, nil
	}
}

func TestDocumentPhotosRouteByWhatTheyShow(t *testing.T) {
	for _, tc := range []struct {
		document string
		tools    []string
		review   string
	}{
		{"letter-finanzamt", []string{"task_create"}, ""},
		{"receipt-cash", []string{"finance_create_manual_transaction"}, ""},
		{"letter-termin", []string{"calendar_create", "task_create"}, ""},
		{"receipt-card", nil, "Card and bank payments arrive from the bank connection."},
		{"letter-aok", nil, "A letter from AOK with no deadline: what should I do with it?"},
	} {
		t.Run(tc.document, func(t *testing.T) {
			tools := &corpusTools{item: inboxItem{ID: "item-" + tc.document, Content: "Photo", Source: "web", MediaReferences: []string{documentRef(tc.document)}, CreatedAt: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}}
			if _, err := (Runner{Planner: documentPlanner{}, Tools: tools, Model: "gpt-5.6-luna", Media: documentMedia{}}).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, tool := range tc.tools {
				if !contains(tools.calls, tool) {
					t.Fatalf("%s not called: %#v", tool, tools.calls)
				}
			}
			if tc.review == "" {
				if !contains(tools.calls, "inbox_resolve") || tools.records[len(tools.records)-1]["status"] != "processed" {
					t.Fatalf("calls = %#v", tools.calls)
				}
				return
			}
			for _, tool := range []string{"task_create", "calendar_create", "finance_create_manual_transaction"} {
				if contains(tools.calls, tool) {
					t.Fatalf("%s called: %#v", tool, tools.calls)
				}
			}
			if contains(tools.calls, "inbox_resolve") || tools.inputs["inbox_set_human_review"]["errorSummary"] != tc.review {
				t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.inputs["inbox_set_human_review"])
			}
		})
	}
}

func TestACashReceiptIsCashSpendingOnItsDate(t *testing.T) {
	tools := &corpusTools{item: inboxItem{ID: "item-receipt", Content: "Photo", Source: "web", MediaReferences: []string{documentRef("receipt-cash")}}}
	if _, err := (Runner{Planner: documentPlanner{}, Tools: tools, Model: "gpt-5.6-luna", Media: documentMedia{}}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	input := tools.inputs["finance_create_manual_transaction"]
	amount, _ := input["amount"].(map[string]any)
	if amount["amount"] != -2347.0 || amount["currency"] != "EUR" || input["counterparty"] != "REWE" || input["occurredOn"] != "2026-10-10" || input["accountId"] != nil || !strings.HasPrefix(input["idempotencyKey"].(string), "action-") {
		t.Fatalf("finance input = %#v", input)
	}
}

func TestImagesArePartOfTheProcessingKey(t *testing.T) {
	text := inboxItem{ID: "item-1", Content: "Call insurer"}
	// A capture without images keeps the key it always had.
	sum := sha256.Sum256([]byte("item-1\x00Call insurer"))
	if got := idempotencyKey(text); got != fmt.Sprintf("codex-%x", sum[:12]) {
		t.Fatalf("text capture key = %s", got)
	}
	letter := inboxItem{ID: "item-2", Content: "Photo", MediaReferences: []string{documentRef("letter-finanzamt")}}
	receipt := inboxItem{ID: "item-2", Content: "Photo", MediaReferences: []string{documentRef("receipt-cash")}}
	both := inboxItem{ID: "item-2", Content: "Photo", MediaReferences: []string{documentRef("letter-finanzamt"), documentRef("receipt-cash")}}
	keys := map[string]bool{idempotencyKey(letter): true, idempotencyKey(receipt): true, idempotencyKey(both): true, idempotencyKey(inboxItem{ID: "item-2", Content: "Photo"}): true}
	if len(keys) != 4 {
		t.Fatalf("photo captures with the same text share keys: %v", keys)
	}
	// The same photo capture, retried, keeps its keys.
	action := plannedAction{Tool: "finance_create_manual_transaction", Arguments: map[string]any{"amount": map[string]any{"amount": -2347.0, "currency": "EUR"}}}
	again := inboxItem{ID: "item-2", Content: "Photo", MediaReferences: []string{documentRef("receipt-cash")}}
	if idempotencyKey(again) != idempotencyKey(receipt) || actionIdempotencyKey(again, 0, action) != actionIdempotencyKey(receipt, 0, action) || actionIdempotencyKey(letter, 0, action) == actionIdempotencyKey(receipt, 0, action) {
		t.Fatal("photo capture action keys are not deterministic per capture")
	}
}

func TestPlanPromptReadsLettersAndReceipts(t *testing.T) {
	for _, want := range []string{
		"Photos of letters and receipts.",
		`the item's text steers and wins over these defaults (\"just save the deadline\" plans only that Task, \"это оплачено\" means nothing is left to pay)`,
		`A text of only \"Photo\" or \"2 photos\" is the placeholder of a capture without words`,
		`A letter with a deadline (\"bis zum 30.10.2026\", \"innerhalb von 14 Tagen ab Zugang\", Frist, Zahlungsziel) is task_create due that day`,
		`titled with the sender as printed and the action, in the language of the item's text, else in English (\"Finanzamt: send tax documents\")`,
		"whether it counts from the letter's printed date or, when it has none, from the capture date",
		"An appointment (Termin with a date, a time, and a place) is calendar_create with the place as location; a letter with both is both actions.",
		`A bill (Rechnung with an amount, an IBAN, and a due date) is task_create \"Pay <sender> <amount>\" due on the due date: Life OS never pays.`,
		"A cash receipt (Kassenbon showing BAR, Bargeld, or cash) is finance_create_manual_transaction with the total (SUMME) as the negative amount in cents",
		`a card receipt (EC, Karte, girocard, Visa, Mastercard, kontaktlos) is a card payment: no action, with reason \"Card and bank payments arrive from the bank connection.\"`,
		"Never copy a document's text into a title or notes: summarize, and keep only the key numbers.",
		`\"A letter from AOK with no deadline: what should I do with it?\"`,
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
