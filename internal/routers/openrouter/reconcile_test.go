package openrouter

import (
	"strings"
	"testing"

	"router-eval/internal/artifacts"
)

func TestGenerationSecond(t *testing.T) {
	sec, ok := GenerationSecond("gen-1782618417-NEpXKqXYIDjS7TmNrT9j")
	if !ok || sec != 1782618417 {
		t.Fatalf("got sec=%d ok=%v", sec, ok)
	}
	if _, ok := GenerationSecond("resp_abc"); ok {
		t.Fatalf("non-gen id should not parse")
	}
}

func TestParseActivityCSV(t *testing.T) {
	csv := "generation_id,created_at,cost_total,tokens_prompt,tokens_completion,model_permaslug,variant\n" +
		"gen-1782618417-billableAAA,2026-06-28 03:46:57.713,0.0123,10389,137,openai/gpt-oss-120b,free\n"
	rows, headers, err := ParseActivityCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(headers) != 7 {
		t.Fatalf("rows=%d headers=%d", len(rows), len(headers))
	}
	r := rows[0]
	if r.GenerationID != "gen-1782618417-billableAAA" || r.CostTotal != 0.0123 || r.TokensPrompt != 10389 || r.TokensCompletion != 137 {
		t.Fatalf("row parsed wrong: %+v", r)
	}
	if sec, _ := r.Second(); sec != 1782618417 {
		t.Fatalf("row second wrong: %d", sec)
	}
}

func TestMatchActivityRows(t *testing.T) {
	// Wire records as captured by the proxy (non-billable wire ids).
	records := []artifacts.RequestRecord{
		{GenerationID: "gen-1782618367-wireA", Context: artifacts.ContextMetrics{InputTokens: 8857, OutputTokens: 1345}},
		{GenerationID: "gen-1782618417-wireB", Context: artifacts.ContextMetrics{InputTokens: 10389, OutputTokens: 137}},
		{GenerationID: "gen-9999999999-wireC", Context: artifacts.ContextMetrics{InputTokens: 5, OutputTokens: 5}}, // no matching row
	}
	rows := []ActivityRow{
		// +1s offset from wireA, exact tokens.
		{GenerationID: "gen-1782618368-billA", TokensPrompt: 8857, TokensCompletion: 1345, ModelPermaslug: "openai/gpt-oss-120b", Variant: "free"},
		// same second as wireB.
		{GenerationID: "gen-1782618417-billB", TokensPrompt: 10389, TokensCompletion: 137, ModelPermaslug: "openai/gpt-oss-120b", Variant: "free"},
	}

	matches := MatchActivityRows(records, rows, "openai/gpt-oss-120b:free")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	byRecord := map[int]ActivityMatch{}
	for _, m := range matches {
		byRecord[m.RecordIndex] = m
	}
	if got := byRecord[0].Row.GenerationID; got != "gen-1782618368-billA" {
		t.Fatalf("record 0 matched %q", got)
	}
	if byRecord[0].SecondsOff != 1 {
		t.Fatalf("record 0 seconds_off = %d, want 1", byRecord[0].SecondsOff)
	}
	if got := byRecord[1].Row.GenerationID; got != "gen-1782618417-billB" {
		t.Fatalf("record 1 matched %q", got)
	}
	if byRecord[0].TokenDistance != 0 || byRecord[1].TokenDistance != 0 {
		t.Fatalf("expected exact token matches")
	}
	if _, ok := byRecord[2]; ok {
		t.Fatalf("record 2 should not match any row")
	}
}

func TestMatchActivityRowsModelFilterAndConsumeOnce(t *testing.T) {
	records := []artifacts.RequestRecord{
		{GenerationID: "gen-1000000000-wire1", Context: artifacts.ContextMetrics{InputTokens: 100, OutputTokens: 10}},
		{GenerationID: "gen-1000000000-wire2", Context: artifacts.ContextMetrics{InputTokens: 200, OutputTokens: 20}},
	}
	rows := []ActivityRow{
		{GenerationID: "gen-1000000000-rowA", TokensPrompt: 100, TokensCompletion: 10, ModelPermaslug: "openai/gpt-oss-120b"},
		{GenerationID: "gen-1000000000-rowB", TokensPrompt: 200, TokensCompletion: 20, ModelPermaslug: "openai/gpt-oss-120b"},
		{GenerationID: "gen-1000000000-other", TokensPrompt: 100, TokensCompletion: 10, ModelPermaslug: "anthropic/claude"},
	}
	matches := MatchActivityRows(records, rows, "openai/gpt-oss-120b")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	// Each row consumed at most once; token proximity routes wire1->rowA, wire2->rowB.
	seen := map[string]bool{}
	for _, m := range matches {
		if seen[m.Row.GenerationID] {
			t.Fatalf("row %q matched twice", m.Row.GenerationID)
		}
		seen[m.Row.GenerationID] = true
		if m.Row.ModelPermaslug == "anthropic/claude" {
			t.Fatalf("model filter let a wrong-model row through")
		}
	}
}
