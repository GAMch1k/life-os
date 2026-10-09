package processor

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// A rate-limit response as returned by Codex CLI 0.160.0 for a ChatGPT Plus
// account (account and credit ids removed).
const rateLimitsResult = `{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":1791591356},"secondary":{"usedPercent":31,"windowDurationMins":10080,"resetsAt":1792138973},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"planType":"plus","rateLimitReachedType":null},"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":1791591356},"secondary":{"usedPercent":31,"windowDurationMins":10080,"resetsAt":1792138973},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"planType":"plus","rateLimitReachedType":null},"base_model_inference":{"limitId":"base_model_inference","limitName":"gpt-reserve","normalModelSlug":"gpt-5.6-luna","primary":{"usedPercent":24,"windowDurationMins":10080,"resetsAt":1791833152},"secondary":null,"credits":null,"planType":"plus","rateLimitReachedType":null}},"rateLimitResetCredits":{"availableCount":2,"credits":[]}}`

// fakeAppServer answers the JSON-RPC requests readUsage sends.
func fakeAppServer(t *testing.T, requests io.Reader, responses io.WriteCloser, limits string) {
	t.Helper()
	// Like an OS pipe, buffer responses so the client can keep writing.
	queue := make(chan string, 16)
	go func() {
		defer responses.Close()
		for line := range queue {
			_, _ = io.WriteString(responses, line+"\n")
		}
	}()
	defer close(queue)
	scanner := bufio.NewScanner(requests)
	write := func(line string) { queue <- line }
	for scanner.Scan() {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Errorf("bad request %q", scanner.Text())
			return
		}
		switch request.Method {
		case "initialize":
			write(`{"id":1,"result":{"userAgent":"test"}}`)
			write(`{"method":"configWarning","params":{"summary":"bubblewrap"}}`)
		case "account/read":
			write(`{"id":2,"result":{"account":{"type":"chatgpt","email":"someone@example.com","planType":"plus"},"requiresOpenaiAuth":true}}`)
		case "account/rateLimits/read":
			write(`{"id":3,` + limits + `}`)
		}
	}
}

func TestReadUsageReportsEveryBucketWithResetTimes(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	go fakeAppServer(t, requestReader, responseWriter, `"result":`+rateLimitsResult)
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	usage, err := readUsage(responseReader, requestWriter, now)
	_ = requestWriter.Close()
	if err != nil {
		t.Fatal(err)
	}
	if usage.AuthMode != "chatgpt" || usage.PlanType != "plus" || !usage.CheckedAt.Equal(now) {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.ResetCreditsAvailable == nil || *usage.ResetCreditsAvailable != 2 {
		t.Fatalf("reset credits = %v", usage.ResetCreditsAvailable)
	}
	if len(usage.Limits) != 2 || usage.Limits[0].ID != "codex" || usage.Limits[1].ID != "base_model_inference" {
		t.Fatalf("limits = %+v", usage.Limits)
	}
	codex := usage.Limits[0]
	if len(codex.Windows) != 2 || codex.Windows[1].UsedPercent != 31 || *codex.Windows[1].WindowMinutes != 10080 {
		t.Fatalf("codex windows = %+v", codex.Windows)
	}
	if !codex.Windows[0].ResetsAt.Equal(time.Unix(1791591356, 0)) || codex.HasCredits == nil || *codex.HasCredits {
		t.Fatalf("codex = %+v", codex)
	}
	reserve := usage.Limits[1]
	if reserve.Name != "gpt-reserve" || reserve.Model != "gpt-5.6-luna" || len(reserve.Windows) != 1 || reserve.Windows[0].UsedPercent != 24 {
		t.Fatalf("reserve = %+v", reserve)
	}
	// The account email is never kept.
	raw, _ := json.Marshal(usage)
	if strings.Contains(string(raw), "example.com") {
		t.Fatalf("usage leaks the account email: %s", raw)
	}
}

func TestReadUsageSurfacesAnErrorInsteadOfGuessing(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	go fakeAppServer(t, requestReader, responseWriter, `"error":{"code":-32600,"message":"not signed in"}`)
	_, err := readUsage(responseReader, requestWriter, time.Now())
	_ = requestWriter.Close()
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if _, err := readUsage(strings.NewReader(""), io.Discard, time.Now()); err == nil {
		t.Fatal("a closed app-server produced usage")
	}
}

func TestMonitorKeepsTheLastGoodUsageWhenAReadFails(t *testing.T) {
	m := NewMonitor("model")
	if m.Snapshot().Usage.Usage != nil {
		t.Fatal("usage before any read")
	}
	m.RecordUsage(Usage{PlanType: "plus", Limits: []UsageLimit{}}, nil)
	m.RecordUsage(Usage{}, io.ErrUnexpectedEOF)
	usage := m.Snapshot().Usage
	if usage.Usage == nil || usage.Usage.PlanType != "plus" || usage.Error == "" {
		t.Fatalf("usage = %+v", usage)
	}
	m.RecordUsage(Usage{PlanType: "pro", Limits: []UsageLimit{}}, nil)
	if usage := m.Snapshot().Usage; usage.Error != "" || usage.Usage.PlanType != "pro" {
		t.Fatalf("usage after recovery = %+v", usage)
	}
}
