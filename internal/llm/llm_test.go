package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"aegis/internal/config"
)

type fakeLedger struct {
	killed    bool
	runTokens int64
	runUSD    float64
	dayUSD    float64
	records   []CallRecord
}

func (f *fakeLedger) KillActive(context.Context, string) (bool, string, error) {
	return f.killed, "test", nil
}
func (f *fakeLedger) RunSpend(context.Context, string, string) (int64, float64, error) {
	return f.runTokens, f.runUSD, nil
}
func (f *fakeLedger) TenantSpendToday(context.Context, string) (float64, error) { return f.dayUSD, nil }
func (f *fakeLedger) RecordLLMCall(_ context.Context, r CallRecord) error {
	f.records = append(f.records, r)
	return nil
}

type fakeProvider struct {
	name  string
	err   error
	msg   string
	calls int
	got   anthropic.MessageNewParams
}

func (p *fakeProvider) Name() string        { return p.name }
func (p *fakeProvider) Model(string) string { return p.name + "-model" }
func (p *fakeProvider) Send(_ context.Context, _ Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	p.calls++
	p.got = params
	if p.err != nil {
		return nil, p.err
	}
	var m anthropic.Message
	if err := m.UnmarshalJSON([]byte(p.msg)); err != nil {
		return nil, err
	}
	return &m, nil
}

const okMsg = `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"hi"}],
"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":4000,"cache_creation_input_tokens":0,"inference_geo":"in"}}`

func router(l *fakeLedger, ps ...Provider) *Router {
	return &Router{Providers: ps, Ledger: l, Timeout: 5 * time.Second,
		Roles:  map[string]config.RoleSettings{"developer": {Effort: "high", MaxTokens: 16000}},
		Budget: config.Budget{PerRunTokens: 100000, PerRunUSD: 5, PerTenantDailyUSD: 50},
		Prices: map[string]Price{"a-model": {Input: 4, Output: 20, CacheRead: 0.2}}}
}

func call(text string) Call {
	return Call{TenantID: "t", RunID: "r", Role: "developer", Params: anthropic.MessageNewParams{
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(text))}}}
}

func TestSuccessAppliesRoleSettingsAndRecordsCost(t *testing.T) {
	l := &fakeLedger{}
	p := &fakeProvider{name: "a", msg: okMsg}
	res, err := router(l, p).Complete(context.Background(), call("fix the bug"))
	if err != nil {
		t.Fatal(err)
	}
	if p.got.MaxTokens != 16000 || p.got.OutputConfig.Effort != "high" || p.got.Model != "a-model" {
		t.Errorf("role settings not applied: %+v", p.got)
	}
	want := (1000*4.0 + 200*20.0 + 4000*0.2) / 1e6
	if res.CostUSD == nil || *res.CostUSD != want {
		t.Errorf("cost = %v, want %v", res.CostUSD, want)
	}
	if len(l.records) != 1 || l.records[0].InferenceGeo != "in" || l.records[0].CacheReadTokens != 4000 {
		t.Errorf("record = %+v", l.records)
	}
}

func TestKillSwitchBlocksBeforeSending(t *testing.T) {
	p := &fakeProvider{name: "a", msg: okMsg}
	_, err := router(&fakeLedger{killed: true}, p).Complete(context.Background(), call("x"))
	if !errors.Is(err, ErrKilled) || p.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

func TestBudgets(t *testing.T) {
	for name, l := range map[string]*fakeLedger{
		"run tokens": {runTokens: 100000}, "run usd": {runUSD: 5}, "tenant day": {dayUSD: 50},
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{name: "a", msg: okMsg}
			if _, err := router(l, p).Complete(context.Background(), call("x")); !errors.Is(err, ErrBudget) || p.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, p.calls)
			}
		})
	}
}

func TestSensitiveDataFailsClosed(t *testing.T) {
	p := &fakeProvider{name: "a", msg: okMsg}
	_, err := router(&fakeLedger{}, p).Complete(context.Background(), call("card 4111 1111 1111 1111"))
	if !errors.Is(err, ErrSensitiveData) || p.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

func TestFallbackOnlyOnRetryableErrors(t *testing.T) {
	down := &fakeProvider{name: "a", err: &anthropic.Error{StatusCode: 503}}
	up := &fakeProvider{name: "b", msg: okMsg}
	res, err := router(&fakeLedger{}, down, up).Complete(context.Background(), call("x"))
	if err != nil || res.Provider != "b" {
		t.Fatalf("fallback failed: %v %+v", err, res)
	}
	bad := &fakeProvider{name: "a", err: &anthropic.Error{StatusCode: 400}}
	up2 := &fakeProvider{name: "b", msg: okMsg}
	if _, err := router(&fakeLedger{}, bad, up2).Complete(context.Background(), call("x")); err == nil || up2.calls != 0 {
		t.Fatalf("400 must not fall through: err=%v calls=%d", err, up2.calls)
	}
	all := router(&fakeLedger{}, &fakeProvider{name: "a", err: &anthropic.Error{StatusCode: 529}})
	if _, err := all.Complete(context.Background(), call("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestRefusalIsAnError(t *testing.T) {
	refused := strings.Replace(okMsg, `"end_turn"`, `"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"no"}`, 1)
	_, err := router(&fakeLedger{}, &fakeProvider{name: "a", msg: refused}).Complete(context.Background(), call("x"))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("want ErrRefused, got %v", err)
	}
}

func TestReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := call("x")
	c.Cassette, c.Attempt, c.Seq = "demo", 1, 0
	rec := &Recorder{Inner: &fakeProvider{name: "a", msg: okMsg}, Dir: dir}
	if _, err := router(&fakeLedger{}, rec).Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo", "developer-a1-000.json")); err != nil {
		t.Fatalf("cassette not written: %v", err)
	}
	rp := NewReplay("replay", dir, map[string]string{"developer": "recorded"})
	res, err := router(&fakeLedger{}, rp).Complete(context.Background(), c)
	if err != nil || res.Message.Content[0].Text != "hi" {
		t.Fatalf("replay: %v %+v", err, res)
	}
	c.Seq = 1
	if _, err := router(&fakeLedger{}, rp).Complete(context.Background(), c); !errors.Is(err, ErrNoCassette) {
		t.Fatalf("missing cassette: %v", err)
	}
}

func TestScanIgnoresJSONEscapes(t *testing.T) {
	p := &fakeProvider{name: "a", msg: okMsg}
	src := "router = APIRouter()\n\n\n@router.post(\"/x\")\ndef f():\n    pass\n"
	if _, err := router(&fakeLedger{}, p).Complete(context.Background(), call(src)); err != nil {
		t.Fatalf("decorator misread as sensitive data: %v", err)
	}
}

func TestBedrockRefusesEnvironmentCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLEEXAMPLE00")
	_, err := NewSDKProvider(context.Background(), config.Provider{Name: "b", Kind: "bedrock", Region: "r", AWSProfile: "p"}, 0)
	if err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Fatalf("environment credentials were not refused: %v", err)
	}
}
