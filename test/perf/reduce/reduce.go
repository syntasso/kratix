// Package reduce produces a human-readable summary from the artefacts a perf
// run leaves behind: a series of /metrics text snapshots and the controller
// log.
//
// Output is a single summary.md in the run dir containing the same shape of
// table that the existing findings doc uses.
package reduce

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RunMeta struct {
	Name          string
	N             int
	Namespace     string
	RRNamePattern string // regexp matching RR names in logs, e.g. ^perf-baseline-2500-\d+$
	WallClock     time.Duration
}

type Summary struct {
	Meta RunMeta

	// from logs
	ReconcilesPerRR []int
	TotalReconciles int

	// from /metrics
	PeakWorkqueueDepth  map[string]float64 // controller -> max depth
	PeakLongestRunning  map[string]float64 // controller -> max longest_running_processor_seconds
	ReconcileTotalEnd   map[string]float64 // controller -> reconcile_total at end
	ReconcileTotalStart map[string]float64
}

func Reduce(runDir string, meta RunMeta) (*Summary, error) {
	s := &Summary{
		Meta:                meta,
		PeakWorkqueueDepth:  map[string]float64{},
		PeakLongestRunning:  map[string]float64{},
		ReconcileTotalEnd:   map[string]float64{},
		ReconcileTotalStart: map[string]float64{},
	}

	logPath := filepath.Join(runDir, "controller.log")
	if _, err := os.Stat(logPath); err == nil {
		rcs, err := reconcilesPerRR(logPath, meta.RRNamePattern)
		if err != nil {
			return nil, fmt.Errorf("count reconciles: %w", err)
		}
		s.ReconcilesPerRR = rcs
		for _, c := range rcs {
			s.TotalReconciles += c
		}
	}

	if err := s.readMetricSnapshots(runDir); err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}

	if err := s.write(runDir); err != nil {
		return nil, fmt.Errorf("write summary: %w", err)
	}
	return s, nil
}

var reconStartRE = regexp.MustCompile(`"reconciliation started"`)

func reconcilesPerRR(logPath, rrPattern string) ([]int, error) {
	rrRE, err := regexp.Compile(rrPattern)
	if err != nil {
		return nil, fmt.Errorf("rr name pattern %q: %w", rrPattern, err)
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !reconStartRE.MatchString(line) {
			continue
		}
		name := rrRE.FindString(line)
		if name == "" {
			continue
		}
		counts[name]++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	out := make([]int, 0, len(counts))
	for _, c := range counts {
		out = append(out, c)
	}
	sort.Ints(out)
	return out, nil
}

// readMetricSnapshots reads every metrics-T+*.prom in runDir and tracks the
// max workqueue_depth / longest_running per controller, plus the first and
// last observed values of controller_runtime_reconcile_total.
func (s *Summary) readMetricSnapshots(runDir string) error {
	matches, err := filepath.Glob(filepath.Join(runDir, "metrics-T+*.prom"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	for i, path := range matches {
		isFirst := i == 0
		isLast := i == len(matches)-1
		if err := s.scanSnapshot(path, isFirst, isLast); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// sample: controller_runtime_reconcile_total{controller="..."} 1234
// sample: workqueue_depth{name="..."} 42
// sample: workqueue_longest_running_processor_seconds{name="..."} 0.085
var seriesRE = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9eE+\-.]+)`)
var labelRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (s *Summary) scanSnapshot(path string, isFirst, isLast bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := seriesRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		metric, labels, valStr := m[1], m[2], m[3]
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil || math.IsNaN(val) {
			continue
		}
		controller := labelValue(labels, "controller")
		if controller == "" {
			controller = labelValue(labels, "name")
		}
		if controller == "" {
			continue
		}
		switch metric {
		case "workqueue_depth":
			if val > s.PeakWorkqueueDepth[controller] {
				s.PeakWorkqueueDepth[controller] = val
			}
		case "workqueue_longest_running_processor_seconds":
			if val > s.PeakLongestRunning[controller] {
				s.PeakLongestRunning[controller] = val
			}
		case "controller_runtime_reconcile_total":
			if isFirst {
				s.ReconcileTotalStart[controller] += val
			}
			if isLast {
				s.ReconcileTotalEnd[controller] += val
			}
		}
	}
	return scanner.Err()
}

func labelValue(labels, key string) string {
	for _, m := range labelRE.FindAllStringSubmatch(labels, -1) {
		if m[1] == key {
			return m[2]
		}
	}
	return ""
}

func (s *Summary) write(runDir string) error {
	path := filepath.Join(runDir, "summary.md")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "# Perf run: %s\n\n", s.Meta.Name)
	fmt.Fprintf(w, "- N (resource requests): %d\n", s.Meta.N)
	fmt.Fprintf(w, "- Namespace: %s\n", s.Meta.Namespace)
	fmt.Fprintf(w, "- Wall-clock to converge: %s\n", s.Meta.WallClock.Truncate(time.Second))
	fmt.Fprintf(w, "- Total reconciles (from log): %d\n", s.TotalReconciles)
	fmt.Fprintln(w)

	p50, p95, p99, mx := percentiles(s.ReconcilesPerRR)
	fmt.Fprintln(w, "## Reconciles per RR (from controller log)")
	fmt.Fprintf(w, "- p50: %d\n", p50)
	fmt.Fprintf(w, "- p95: %d\n", p95)
	fmt.Fprintf(w, "- p99: %d\n", p99)
	fmt.Fprintf(w, "- max: %d\n", mx)
	fmt.Fprintf(w, "- sample size: %d RRs matched\n", len(s.ReconcilesPerRR))
	fmt.Fprintln(w)

	controllers := keysUnion(s.PeakWorkqueueDepth, s.PeakLongestRunning, s.ReconcileTotalEnd)
	sort.Strings(controllers)

	fmt.Fprintln(w, "## Per-controller peaks (from /metrics snapshots)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| controller | peak queue depth | peak longest running (s) | reconcile_total Δ |")
	fmt.Fprintln(w, "|---|---:|---:|---:|")
	for _, c := range controllers {
		delta := s.ReconcileTotalEnd[c] - s.ReconcileTotalStart[c]
		fmt.Fprintf(w, "| %s | %.0f | %.3f | %.0f |\n",
			c, s.PeakWorkqueueDepth[c], s.PeakLongestRunning[c], delta)
	}
	return nil
}

func percentiles(xs []int) (p50, p95, p99, mx int) {
	if len(xs) == 0 {
		return 0, 0, 0, 0
	}
	// xs is already sorted ascending in reconcilesPerRR
	pick := func(p float64) int {
		idx := int(math.Ceil(p*float64(len(xs)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(xs) {
			idx = len(xs) - 1
		}
		return xs[idx]
	}
	return pick(0.50), pick(0.95), pick(0.99), xs[len(xs)-1]
}

func keysUnion(maps ...map[string]float64) []string {
	seen := map[string]struct{}{}
	for _, m := range maps {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
