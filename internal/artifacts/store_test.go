package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreWritesManifestAndRequests(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	err = store.WriteManifest(Manifest{
		RunID:     "run-1",
		Router:    "tokenrouter",
		Workload:  WorkloadRef{Kind: "probe", Name: "latency_smoke"},
		Status:    RunStatusRunning,
		StartedAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run-1", "manifest.json")); err != nil {
		t.Fatal(err)
	}

	err = store.AppendRequest(RequestRecord{
		SchemaVersion: 1,
		RunID:         "run-1",
		Router:        "tokenrouter",
		Method:        "POST",
		Endpoint:      "/v1/responses",
		StatusCode:    200,
		Success:       true,
		StartedAt:     time.Unix(1, 0).UTC(),
		Usage:         Usage{CostState: CostStatePending},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.RequestsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"run_id":"run-1"`) {
		t.Fatalf("request record missing run id: %s", data)
	}
}

func TestStoreUpdateIndexUpsertsAndSorts(t *testing.T) {
	root := t.TempDir()
	originalNow := timeNowUTC
	timeNowUTC = func() time.Time { return time.Unix(10, 0).UTC() }
	defer func() { timeNowUTC = originalNow }()

	storeA, err := NewStore(root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(root, "run-b")
	if err != nil {
		t.Fatal(err)
	}

	manifestA := Manifest{
		RunID:     "run-a",
		Router:    "openrouter",
		Workload:  WorkloadRef{Kind: "probe/responses", Name: "basic_responses"},
		Status:    RunStatusDone,
		StartedAt: time.Unix(1, 0).UTC(),
	}
	manifestB := Manifest{
		RunID:     "run-b",
		Router:    "tokenrouter",
		Workload:  WorkloadRef{Kind: "probe/chat", Name: "latency_smoke"},
		Status:    RunStatusDone,
		StartedAt: time.Unix(2, 0).UTC(),
	}

	if err := storeA.UpdateIndex(manifestA, Metrics{RunID: "run-a", Comparable: RunComparableRollup{TotalCostUSD: 0.1}, Context: RunContextRollup{RequestCount: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := storeB.UpdateIndex(manifestB, Metrics{RunID: "run-b", Comparable: RunComparableRollup{TotalCostUSD: 0.2}, Context: RunContextRollup{RequestCount: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.UpdateIndex(manifestA, Metrics{RunID: "run-a", Comparable: RunComparableRollup{TotalCostUSD: 0.3}, Context: RunContextRollup{RequestCount: 3}}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(storeA.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, `"run_id": "run-a"`) != 1 {
		t.Fatalf("expected run-a to be upserted once:\n%s", text)
	}
	if !strings.Contains(text, `"total_cost_usd": 0.3`) {
		t.Fatalf("expected updated cost:\n%s", text)
	}
	if _, err := os.Stat(storeA.UIAggregatePath()); err != nil {
		t.Fatalf("expected aggregate artifact: %v", err)
	}
	if strings.Index(text, `"run_id": "run-b"`) > strings.Index(text, `"run_id": "run-a"`) {
		t.Fatalf("expected newest run first:\n%s", text)
	}
}

func TestBuildUIAggregateGroupsProbesAndHarnesses(t *testing.T) {
	runs := []RunCatalog{
		{
			RunID: "probe-1", Router: "openrouter", Workload: WorkloadRef{Kind: "probe/responses", Name: "basic_responses"},
			Summary: RunCatalogSummary{RequestCount: 1, TotalCostUSD: 0.1, SuccessRate: 1},
		},
		{
			RunID: "probe-2", Router: "tokenrouter", Workload: WorkloadRef{Kind: "probe/chat", Name: "multi_latency"},
			Summary: RunCatalogSummary{RequestCount: 2, TotalCostUSD: 0.2, SuccessRate: 0.5},
		},
		{
			RunID: "harness-1", Router: "openrouter", Workload: WorkloadRef{Kind: "harness/codex", Name: "issue-123"},
			Summary: RunCatalogSummary{RequestCount: 3, TotalCostUSD: 0.3, SuccessRate: 1},
		},
	}
	aggregate := BuildUIAggregate(runs)
	if len(aggregate.Probes) != 2 {
		t.Fatalf("probe groups=%+v", aggregate.Probes)
	}
	if aggregate.Probes[0].Router != "openrouter" || aggregate.Probes[0].Workloads[0].Type != "single" {
		t.Fatalf("openrouter group=%+v", aggregate.Probes[0])
	}
	if aggregate.Probes[1].Router != "tokenrouter" || aggregate.Probes[1].Workloads[0].Type != "multi" {
		t.Fatalf("tokenrouter group=%+v", aggregate.Probes[1])
	}
	if len(aggregate.Harnesses) != 1 || aggregate.Harnesses[0].Harness != "codex" {
		t.Fatalf("harness groups=%+v", aggregate.Harnesses)
	}
	if aggregate.Harnesses[0].Routers[0].Tasks[0].Task != "issue-123" {
		t.Fatalf("harness task=%+v", aggregate.Harnesses[0].Routers[0].Tasks)
	}
}
