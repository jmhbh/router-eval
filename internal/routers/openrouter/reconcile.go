package openrouter

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"

	"router-eval/internal/artifacts"
	"router-eval/internal/usage"
)

// Reconciliation from the OpenRouter activity CSV export.
//
// For genuine Codex /v1/responses traffic, the generation id returned on the wire
// (X-Generation-Id / SSE response.id) is NOT the id OpenRouter bills under: the
// presence of a web_search tool and a developer-role input message each cause
// OpenRouter to route through a wrapped generation, so the billable id differs and
// is not exposed on the wire. The live /api/v1/generation lookup therefore can't
// reconcile these runs, and the billable id lives only in the activity log.
//
// Both ids are formatted gen-<unix_second>-<random> and share the same embedded
// creation second (+/-1s). The captured inline usage also matches the billable
// row's token counts exactly. So we reconcile by matching the second embedded in
// the wire id against the activity row's second, then model, disambiguating by
// token-count proximity. The user downloads the activity CSV manually and passes
// its path to `reconcile --csv`.

// ActivityRow is one row of the OpenRouter activity CSV export.
type ActivityRow struct {
	GenerationID     string            `json:"generation_id"`
	CreatedAt        string            `json:"created_at,omitempty"`
	CostTotal        float64           `json:"cost_total"`
	TokensPrompt     int               `json:"tokens_prompt,omitempty"`
	TokensCompletion int               `json:"tokens_completion,omitempty"`
	TokensReasoning  int               `json:"tokens_reasoning,omitempty"`
	TokensCached     int               `json:"tokens_cached,omitempty"`
	ModelPermaslug   string            `json:"model_permaslug,omitempty"`
	Variant          string            `json:"variant,omitempty"`
	ProviderName     string            `json:"provider_name,omitempty"`
	Cancelled        bool              `json:"cancelled,omitempty"`
	Streamed         bool              `json:"streamed,omitempty"`
	Raw              map[string]string `json:"raw,omitempty"`
}

// Second returns the creation second for the row, preferring the one embedded in
// the billable generation id and falling back to parsing created_at.
func (r ActivityRow) Second() (int64, bool) {
	if sec, ok := GenerationSecond(r.GenerationID); ok {
		return sec, true
	}
	return createdAtSecond(r.CreatedAt)
}

// LookupResult converts an activity row into a usage_api_confirmed cost record.
func (r ActivityRow) LookupResult() usage.LookupResult {
	return usage.LookupResult{
		RequestID:    r.GenerationID,
		GenerationID: r.GenerationID,
		Found:        true,
		Raw:          r,
		Usage: artifacts.Usage{
			InputTokens:     r.TokensPrompt,
			OutputTokens:    r.TokensCompletion,
			CacheReadTokens: r.TokensCached,
			TotalTokens:     r.TokensPrompt + r.TokensCompletion,
			CostUSD:         r.CostTotal,
			CostKnown:       true,
			CostState:       artifacts.CostStateUsageAPIConfirmed,
			Raw:             r,
		},
	}
}

// GenerationSecond extracts the unix second embedded in an OpenRouter generation
// id of the form gen-<unix_second>-<random>. The same second is present in both
// the wire id and the billable id, which is what makes reconciliation possible
// without the (unexposed) billable id.
func GenerationSecond(id string) (int64, bool) {
	if !strings.HasPrefix(id, "gen-") {
		return 0, false
	}
	parts := strings.SplitN(id, "-", 3)
	if len(parts) < 2 {
		return 0, false
	}
	sec, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || sec <= 0 {
		return 0, false
	}
	return sec, true
}

func createdAtSecond(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Unix(), true
		}
	}
	return 0, false
}

// ParseActivityCSV parses an OpenRouter activity CSV export into rows.
func ParseActivityCSV(r io.Reader) ([]ActivityRow, []string, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	header := map[string]int{}
	headers := make([]string, 0, len(records[0]))
	for i, name := range records[0] {
		normalized := normalizeHeader(name)
		header[normalized] = i
		headers = append(headers, normalized)
	}
	rows := make([]ActivityRow, 0, len(records)-1)
	for _, record := range records[1:] {
		row := ActivityRow{
			GenerationID:     csvStr(record, header, "generation_id", "id"),
			CreatedAt:        csvStr(record, header, "created_at", "timestamp"),
			CostTotal:        csvFloat(record, header, "cost_total", "total_cost", "cost"),
			TokensPrompt:     csvInt(record, header, "tokens_prompt", "native_tokens_prompt", "prompt_tokens"),
			TokensCompletion: csvInt(record, header, "tokens_completion", "native_tokens_completion", "completion_tokens"),
			TokensReasoning:  csvInt(record, header, "tokens_reasoning", "native_tokens_reasoning"),
			TokensCached:     csvInt(record, header, "tokens_cached", "cache_tokens", "cached_tokens"),
			ModelPermaslug:   csvStr(record, header, "model_permaslug", "model"),
			Variant:          csvStr(record, header, "variant"),
			ProviderName:     csvStr(record, header, "provider_name", "provider"),
			Cancelled:        csvBool(record, header, "cancelled"),
			Streamed:         csvBool(record, header, "streamed"),
			Raw:              map[string]string{},
		}
		for name, idx := range header {
			if idx >= 0 && idx < len(record) {
				row.Raw[name] = record[idx]
			}
		}
		if row.GenerationID != "" {
			rows = append(rows, row)
		}
	}
	return rows, headers, nil
}

// ActivityMatch pairs one request record (by index) to an activity row.
type ActivityMatch struct {
	RecordIndex   int         `json:"record_index"`
	Row           ActivityRow `json:"-"`
	Second        int64       `json:"created_second"`
	SecondsOff    int64       `json:"seconds_off"`
	TokenDistance int         `json:"token_distance"`
	Strategy      string      `json:"strategy"`
}

// MatchActivityRows pairs request records to activity rows using the unix second
// embedded in the gen-<sec>- id (shared by wire and billable id), then model, then
// token-count proximity to disambiguate. Each activity row is consumed at most
// once. model may be empty to skip model filtering.
func MatchActivityRows(records []artifacts.RequestRecord, rows []ActivityRow, model string) []ActivityMatch {
	rowSecond := make([]int64, len(rows))
	bySecond := map[int64][]int{}
	for i, row := range rows {
		sec, ok := row.Second()
		if !ok {
			rowSecond[i] = -1
			continue
		}
		rowSecond[i] = sec
		bySecond[sec] = append(bySecond[sec], i)
	}

	used := make([]bool, len(rows))
	var matches []ActivityMatch
	for ri := range records {
		recSec, ok := recordSecond(records[ri])
		if !ok {
			continue
		}
		type candidate struct {
			idx    int
			secOff int64
			tok    int
		}
		var cands []candidate
		for _, off := range []int64{0, -1, 1} {
			for _, idx := range bySecond[recSec+off] {
				if used[idx] || !modelMatches(model, rows[idx]) {
					continue
				}
				secOff := off
				if secOff < 0 {
					secOff = -secOff
				}
				cands = append(cands, candidate{idx: idx, secOff: secOff, tok: tokenDistance(records[ri], rows[idx])})
			}
		}
		if len(cands) == 0 {
			continue
		}
		best := 0
		for i := 1; i < len(cands); i++ {
			if cands[i].secOff < cands[best].secOff ||
				(cands[i].secOff == cands[best].secOff && cands[i].tok < cands[best].tok) {
				best = i
			}
		}
		chosen := cands[best]
		used[chosen.idx] = true
		strategy := "generation_second+model"
		if len(cands) > 1 {
			strategy = "generation_second+model+tokens"
		}
		matches = append(matches, ActivityMatch{
			RecordIndex:   ri,
			Row:           rows[chosen.idx],
			Second:        rowSecond[chosen.idx],
			SecondsOff:    chosen.secOff,
			TokenDistance: chosen.tok,
			Strategy:      strategy,
		})
	}
	return matches
}

func recordSecond(record artifacts.RequestRecord) (int64, bool) {
	for _, id := range []string{record.GenerationID, record.RequestID} {
		if sec, ok := GenerationSecond(id); ok {
			return sec, true
		}
	}
	return 0, false
}

func modelMatches(model string, row ActivityRow) bool {
	if model == "" || row.ModelPermaslug == "" {
		return true
	}
	if strings.EqualFold(baseModel(model), baseModel(row.ModelPermaslug)) {
		return true
	}
	return strings.EqualFold(model, row.ModelPermaslug+":"+row.Variant)
}

func baseModel(model string) string {
	if i := strings.IndexByte(model, ':'); i >= 0 {
		return model[:i]
	}
	return model
}

func tokenDistance(record artifacts.RequestRecord, row ActivityRow) int {
	return absInt(row.TokensPrompt-record.Context.InputTokens) + absInt(row.TokensCompletion-record.Context.OutputTokens)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func normalizeHeader(value string) string {
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(value)
}

func csvStr(record []string, header map[string]int, names ...string) string {
	for _, name := range names {
		if idx, ok := header[name]; ok && idx >= 0 && idx < len(record) {
			return strings.TrimSpace(record[idx])
		}
	}
	return ""
}

func csvInt(record []string, header map[string]int, names ...string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(csvStr(record, header, names...), ",", ""))
	return n
}

func csvFloat(record []string, header map[string]int, names ...string) float64 {
	n, _ := strconv.ParseFloat(strings.ReplaceAll(csvStr(record, header, names...), ",", ""), 64)
	return n
}

func csvBool(record []string, header map[string]int, names ...string) bool {
	return strings.EqualFold(csvStr(record, header, names...), "true")
}
