package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"router-eval/internal/artifacts"
)

func TestServerServesRunDetail(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewStore(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := artifacts.Manifest{
		RunID:     "run-1",
		Router:    "openrouter",
		Workload:  artifacts.WorkloadRef{Kind: "probe/responses", Name: "basic_responses"},
		Status:    artifacts.RunStatusDone,
		StartedAt: time.Unix(1, 0).UTC(),
	}
	if err := store.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMetrics(artifacts.Metrics{RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRequest(artifacts.RequestRecord{RunID: "run-1", Router: "openrouter"}); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail RunDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Manifest.RunID != "run-1" || len(detail.Requests) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestServerServesEmptyIndex(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/index", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"runs": []`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestServerScansIndexWhenCatalogMissing(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewStore(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteManifest(artifacts.Manifest{
		RunID:     "run-1",
		Router:    "openrouter",
		Workload:  artifacts.WorkloadRef{Kind: "probe/responses", Name: "basic_responses"},
		Status:    artifacts.RunStatusDone,
		StartedAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMetrics(artifacts.Metrics{
		RunID:      "run-1",
		Comparable: artifacts.RunComparableRollup{SuccessRate: 1},
		Context:    artifacts.RunContextRollup{RequestCount: 1},
	}); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/index", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"run_id": "run-1"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestServerServesAggregate(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewStore(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := artifacts.Manifest{
		RunID:     "run-1",
		Router:    "openrouter",
		Workload:  artifacts.WorkloadRef{Kind: "probe/responses", Name: "basic_responses"},
		Status:    artifacts.RunStatusDone,
		StartedAt: time.Unix(1, 0).UTC(),
	}
	metrics := artifacts.Metrics{
		RunID:      "run-1",
		Comparable: artifacts.RunComparableRollup{SuccessRate: 1},
		Context:    artifacts.RunContextRollup{RequestCount: 1},
	}
	if err := store.UpdateIndex(manifest, metrics); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/aggregate", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"probes"`) || !strings.Contains(rec.Body.String(), `"openrouter"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestServerAggregateMergesIndexWithRunDirectories(t *testing.T) {
	root := t.TempDir()
	indexedStore, err := artifacts.NewStore(root, "harness-1")
	if err != nil {
		t.Fatal(err)
	}
	indexedManifest := artifacts.Manifest{
		RunID:     "harness-1",
		Router:    "tokenrouter",
		Workload:  artifacts.WorkloadRef{Kind: "harness/codex", Name: "task-1"},
		Status:    artifacts.RunStatusDone,
		StartedAt: time.Unix(2, 0).UTC(),
	}
	if err := indexedStore.UpdateIndex(indexedManifest, artifacts.Metrics{
		RunID:      "harness-1",
		Comparable: artifacts.RunComparableRollup{SuccessRate: 1},
		Context:    artifacts.RunContextRollup{RequestCount: 1},
	}); err != nil {
		t.Fatal(err)
	}

	probeStore, err := artifacts.NewStore(root, "probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := probeStore.WriteManifest(artifacts.Manifest{
		RunID:     "probe-1",
		Router:    "openrouter",
		Workload:  artifacts.WorkloadRef{Kind: "probe/chat", Name: "latency_smoke"},
		Status:    artifacts.RunStatusDone,
		StartedAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := probeStore.WriteMetrics(artifacts.Metrics{
		RunID:      "probe-1",
		Comparable: artifacts.RunComparableRollup{SuccessRate: 1},
		Context:    artifacts.RunContextRollup{RequestCount: 1},
	}); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/aggregate", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"harness": "codex"`) || !strings.Contains(body, `"router": "openrouter"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestServerRejectsInvalidRunID(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/extra", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestReadRequestsMissingFile(t *testing.T) {
	_, err := readRequests("/does/not/exist")
	if !os.IsNotExist(err) {
		t.Fatalf("err=%v", err)
	}
}
