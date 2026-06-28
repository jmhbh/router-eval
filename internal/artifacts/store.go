package artifacts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	root  string
	runID string
	mu    sync.Mutex
}

func RootIndexPath(root string) string {
	if root == "" {
		root = "out"
	}
	return filepath.Join(root, "index.json")
}

func RootUIAggregatePath(root string) string {
	if root == "" {
		root = "out"
	}
	return filepath.Join(root, "aggregates", "ui.json")
}

func NewStore(root, runID string) (*Store, error) {
	if root == "" {
		root = "out"
	}
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	store := &Store{root: root, runID: runID}
	if err := os.MkdirAll(store.RawDir(), 0o755); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) RunDir() string {
	return filepath.Join(s.root, "runs", s.runID)
}

func (s *Store) RawDir() string {
	return filepath.Join(s.RunDir(), "raw")
}

func (s *Store) RequestsPath() string {
	return filepath.Join(s.RunDir(), "requests.jsonl")
}

func (s *Store) ManifestPath() string {
	return filepath.Join(s.RunDir(), "manifest.json")
}

func (s *Store) MetricsPath() string {
	return filepath.Join(s.RunDir(), "metrics.json")
}

func (s *Store) IndexPath() string {
	return RootIndexPath(s.root)
}

func (s *Store) UIAggregatePath() string {
	return RootUIAggregatePath(s.root)
}

func (s *Store) WriteManifest(manifest Manifest) error {
	return writeJSONFile(s.ManifestPath(), manifest)
}

func (s *Store) WriteMetrics(metrics Metrics) error {
	return writeJSONFile(s.MetricsPath(), metrics)
}

func (s *Store) UpdateIndex(manifest Manifest, metrics Metrics) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := Index{}
	data, err := os.ReadFile(s.IndexPath())
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &index); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	entry := RunCatalog{
		RunID:     manifest.RunID,
		Router:    manifest.Router,
		Model:     manifest.Model,
		Workload:  manifest.Workload,
		Status:    manifest.Status,
		StartedAt: manifest.StartedAt,
		EndedAt:   manifest.EndedAt,
		Summary: RunCatalogSummary{
			TotalCostUSD:       metrics.Comparable.TotalCostUSD,
			TotalCostKnown:     metrics.Comparable.TotalCostKnown,
			TTFTP50Millis:      metrics.Comparable.TTFTP50Millis,
			E2EP50Millis:       metrics.Comparable.E2EP50Millis,
			E2EP95Millis:       metrics.Comparable.E2EP95Millis,
			TotalRequestMillis: metrics.Comparable.TotalRequestMillis,
			SuccessRate:        metrics.Comparable.SuccessRate,
			RequestCount:       metrics.Context.RequestCount,
			InputTokens:        metrics.Context.InputTokens,
			OutputTokens:       metrics.Context.OutputTokens,
			ToolCallCount:      metrics.Context.ToolCallCount,
		},
	}

	replaced := false
	for i, existing := range index.Runs {
		if existing.RunID == entry.RunID {
			index.Runs[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		index.Runs = append(index.Runs, entry)
	}
	index.UpdatedAt = timeNowUTC()
	sortRuns(index.Runs)
	if err := writeJSONFile(s.IndexPath(), index); err != nil {
		return err
	}
	return s.WriteUIAggregate(BuildUIAggregate(index.Runs))
}

func (s *Store) WriteUIAggregate(aggregate UIAggregate) error {
	return writeJSONFile(s.UIAggregatePath(), aggregate)
}

func (s *Store) AppendRequest(record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.RunDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.RequestsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	return enc.Encode(record)
}

func (s *Store) ReadRequests() ([]RequestRecord, error) {
	f, err := os.Open(s.RequestsPath())
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []RequestRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var record RequestRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func (s *Store) WriteRequests(records []RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.RunDir(), 0o755); err != nil {
		return err
	}
	tmp := s.RequestsPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.RequestsPath())
}

func (s *Store) RawCapturePath(name string) string {
	if name == "" {
		name = "unknown"
	}
	return filepath.Join(s.RawDir(), name+".body")
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sortRuns(runs []RunCatalog) {
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
}

func BuildUIAggregate(runs []RunCatalog) UIAggregate {
	return UIAggregate{
		UpdatedAt: timeNowUTC(),
		Probes:    buildProbeGroups(runs),
		Harnesses: buildHarnessGroups(runs),
	}
}

func buildProbeGroups(runs []RunCatalog) []ProbeRouterGroup {
	byRouter := map[string][]RunCatalog{}
	for _, run := range runs {
		if strings.HasPrefix(run.Workload.Kind, "probe/") {
			byRouter[run.Router] = append(byRouter[run.Router], run)
		}
	}
	routerNames := sortedKeys(byRouter)
	groups := make([]ProbeRouterGroup, 0, len(routerNames))
	for _, router := range routerNames {
		routerRuns := byRouter[router]
		byType := map[string][]RunCatalog{}
		for _, run := range routerRuns {
			byType[probeWorkloadType(run)] = append(byType[probeWorkloadType(run)], run)
		}
		typeNames := sortedKeys(byType)
		workloads := make([]ProbeWorkloadGroup, 0, len(typeNames))
		for _, typeName := range typeNames {
			workloadRuns := byType[typeName]
			byProbe := map[string][]RunCatalog{}
			for _, run := range workloadRuns {
				byProbe[run.Workload.Name] = append(byProbe[run.Workload.Name], run)
			}
			probeNames := sortedKeys(byProbe)
			probes := make([]ProbeRunGroup, 0, len(probeNames))
			for _, probeName := range probeNames {
				probeRuns := byProbe[probeName]
				sortRuns(probeRuns)
				probes = append(probes, ProbeRunGroup{
					Name:    probeName,
					Runs:    probeRuns,
					Summary: summarizeRuns(probeRuns),
				})
			}
			workloads = append(workloads, ProbeWorkloadGroup{
				Type:    typeName,
				Summary: summarizeRuns(workloadRuns),
				Probes:  probes,
			})
		}
		groups = append(groups, ProbeRouterGroup{
			Router:    router,
			Summary:   summarizeRuns(routerRuns),
			Workloads: workloads,
		})
	}
	return groups
}

func buildHarnessGroups(runs []RunCatalog) []HarnessGroup {
	byHarness := map[string][]RunCatalog{}
	for _, run := range runs {
		if !strings.HasPrefix(run.Workload.Kind, "harness/") {
			continue
		}
		harness := strings.TrimPrefix(run.Workload.Kind, "harness/")
		if harness == "" {
			harness = "unknown"
		}
		byHarness[harness] = append(byHarness[harness], run)
	}
	harnessNames := sortedKeys(byHarness)
	groups := make([]HarnessGroup, 0, len(harnessNames))
	for _, harness := range harnessNames {
		harnessRuns := byHarness[harness]
		sortRuns(harnessRuns)
		byRouter := map[string][]RunCatalog{}
		for _, run := range harnessRuns {
			byRouter[run.Router] = append(byRouter[run.Router], run)
		}
		routerNames := sortedKeys(byRouter)
		routers := make([]HarnessRouterGroup, 0, len(routerNames))
		for _, router := range routerNames {
			routerRuns := byRouter[router]
			byTask := map[string][]RunCatalog{}
			for _, run := range routerRuns {
				byTask[run.Workload.Name] = append(byTask[run.Workload.Name], run)
			}
			taskNames := sortedKeys(byTask)
			tasks := make([]HarnessTaskGroup, 0, len(taskNames))
			for _, task := range taskNames {
				taskRuns := byTask[task]
				sortRuns(taskRuns)
				tasks = append(tasks, HarnessTaskGroup{
					Task:    task,
					Runs:    taskRuns,
					Summary: summarizeRuns(taskRuns),
				})
			}
			routers = append(routers, HarnessRouterGroup{
				Router:  router,
				Summary: summarizeRuns(routerRuns),
				Tasks:   tasks,
			})
		}
		groups = append(groups, HarnessGroup{
			Harness: harness,
			Summary: summarizeRuns(harnessRuns),
			Runs:    harnessRuns,
			Routers: routers,
		})
	}
	return groups
}

func probeWorkloadType(run RunCatalog) string {
	if strings.Contains(run.Workload.Name, "multi") {
		return "multi"
	}
	return "single"
}

func summarizeRuns(runs []RunCatalog) AggregateSummary {
	summary := AggregateSummary{RunCount: len(runs)}
	var weight int
	for _, run := range runs {
		s := run.Summary
		summary.RequestCount += s.RequestCount
		if s.TotalCostKnown {
			summary.TotalCostUSD += s.TotalCostUSD
			summary.TotalCostKnown = true
		}
		summary.InputTokens += s.InputTokens
		summary.OutputTokens += s.OutputTokens
		summary.ToolCallCount += s.ToolCallCount
		w := s.RequestCount
		if w <= 0 {
			w = 1
		}
		weight += w
		summary.TTFTP50Millis += s.TTFTP50Millis * float64(w)
		summary.E2EP50Millis += s.E2EP50Millis * float64(w)
		summary.E2EP95Millis += s.E2EP95Millis * float64(w)
		summary.TotalRequestMillis += s.TotalRequestMillis
		summary.SuccessRate += s.SuccessRate * float64(w)
	}
	if weight > 0 {
		summary.TTFTP50Millis /= float64(weight)
		summary.E2EP50Millis /= float64(weight)
		summary.E2EP95Millis /= float64(weight)
		summary.SuccessRate /= float64(weight)
	}
	return summary
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var timeNowUTC = func() time.Time {
	return time.Now().UTC()
}
