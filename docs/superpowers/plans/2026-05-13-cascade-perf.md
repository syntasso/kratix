# Cascade Perf Scenario Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `cascade` perf scenario to the existing rig that drives N `SimpleApp` resource requests through a 3-Promise cascade (`SimpleApp → AppAsAService → EvoPublisher`) and measures controller behaviour end-to-end.

**Architecture:** New `It` block (`Label("cascade")`) in `test/perf/cascade_test.go`. Existing no-op spec gets `Label("noop")`. Driver gains two helpers (`ApplyPromises`, `WaitForCascadeConverged`) without touching the existing single-Promise path. Reducer gains an additive `RRNamePatterns []NamedPattern` field that, when set, emits one percentile section per pattern; absent, the existing single-pattern output is unchanged.

**Tech Stack:** Go 1.22+, Ginkgo v2, controller-runtime client, busybox `sh -c` bash mocks for pipeline containers.

---

## File Structure

**New files:**
- `test/perf/assets/cascade-promises.yaml` — 3 Promises in one multi-doc YAML
- `test/perf/cascade_test.go` — the cascade `It` block (build-tagged `perf`)

**Modified files:**
- `test/perf/driver/promise.go` — add `ApplyPromises(...)` (multi-doc YAML splitter)
- `test/perf/driver/convergence.go` — add `WaitForCascadeConverged(...)`
- `test/perf/reduce/reduce.go` — add `NamedPattern` type, extend `RunMeta` with `RRNamePatterns`, branch in `Reduce`/`write` when non-empty
- `test/perf/perf_test.go` — wrap existing `It` with `Label("noop")`
- `Makefile` — add `perf-test-cascade` target, update `perf-test` to filter on `noop` label
- `test/perf/README.md` — short paragraph on the cascade scenario

Each modification is additive — the existing no-op path keeps running exactly as before.

---

## Task 1: Reducer — add `NamedPattern` type and `RRNamePatterns` field

**Why first**: every later piece references the new type. We can land this without behaviour change first, because if `RRNamePatterns` is empty the existing code path is preserved verbatim.

**Files:**
- Modify: `test/perf/reduce/reduce.go:22-28` (RunMeta struct)
- Modify: `test/perf/reduce/reduce.go:44-73` (Reduce)
- Modify: `test/perf/reduce/reduce.go:196-235` (write)

- [ ] **Step 1.1: Add `NamedPattern` type and extend `RunMeta`**

Replace the `RunMeta` struct block (currently at `reduce.go:22-28`) with:

```go
type RunMeta struct {
	Name      string
	N         int
	Namespace string
	// RRNamePattern is a single regexp matching RR names in the controller
	// log. Used by the no-op scenario where every RR is the same kind.
	RRNamePattern string
	// RRNamePatterns lists one labelled regexp per RR kind in the run, used
	// by cascade-style scenarios with multiple RR types. When non-empty,
	// it takes precedence over RRNamePattern and the reducer emits one
	// "Reconciles per RR" subsection per entry.
	RRNamePatterns []NamedPattern
	WallClock      time.Duration
}

// NamedPattern attaches a human-readable label to an RR-name regexp.
// The label is used as the subsection heading in summary.md.
type NamedPattern struct {
	Label   string
	Pattern string
}
```

- [ ] **Step 1.2: Extend `Summary` to hold per-pattern reconcile counts**

Find the `Summary` struct (currently at `reduce.go:30-42`). Replace the two
log-derived fields (`ReconcilesPerRR []int`, `TotalReconciles int`) with:

```go
type Summary struct {
	Meta RunMeta

	// from logs (single-pattern path)
	ReconcilesPerRR []int
	TotalReconciles int
	// from logs (multi-pattern path); keyed by NamedPattern.Label
	ReconcilesByPattern map[string][]int

	// from /metrics (unchanged)
	PeakWorkqueueDepth  map[string]float64
	PeakLongestRunning  map[string]float64
	ReconcileTotalEnd   map[string]float64
	ReconcileTotalStart map[string]float64
}
```

- [ ] **Step 1.3: Branch in `Reduce` based on which field is set**

Replace the log-reading block (currently `reduce.go:53-63` — the
`if _, err := os.Stat(logPath); err == nil { ... }`) with:

```go
logPath := filepath.Join(runDir, "controller.log")
if _, err := os.Stat(logPath); err == nil {
	if len(meta.RRNamePatterns) > 0 {
		s.ReconcilesByPattern = map[string][]int{}
		for _, np := range meta.RRNamePatterns {
			rcs, err := reconcilesPerRR(logPath, np.Pattern)
			if err != nil {
				return nil, fmt.Errorf("count reconciles for %q: %w", np.Label, err)
			}
			s.ReconcilesByPattern[np.Label] = rcs
			for _, c := range rcs {
				s.TotalReconciles += c
			}
		}
	} else {
		rcs, err := reconcilesPerRR(logPath, meta.RRNamePattern)
		if err != nil {
			return nil, fmt.Errorf("count reconciles: %w", err)
		}
		s.ReconcilesPerRR = rcs
		for _, c := range rcs {
			s.TotalReconciles += c
		}
	}
}
```

Also initialise `ReconcilesByPattern` in the constructor at `reduce.go:45-51`:

```go
s := &Summary{
	Meta:                meta,
	ReconcilesByPattern: map[string][]int{},
	PeakWorkqueueDepth:  map[string]float64{},
	PeakLongestRunning:  map[string]float64{},
	ReconcileTotalEnd:   map[string]float64{},
	ReconcileTotalStart: map[string]float64{},
}
```

- [ ] **Step 1.4: Branch in `write` to emit per-pattern subsections**

Inside `(s *Summary) write` at `reduce.go:196`, find the "Reconciles per RR"
fprintf block (currently lines 213–220). Replace with:

```go
fmt.Fprintln(w, "## Reconciles per RR (from controller log)")
if len(s.Meta.RRNamePatterns) > 0 {
	// emit subsections in declared order so summary diffs are stable
	for _, np := range s.Meta.RRNamePatterns {
		xs := s.ReconcilesByPattern[np.Label]
		p50, p95, p99, mx := percentiles(xs)
		fmt.Fprintf(w, "### %s\n", np.Label)
		fmt.Fprintf(w, "- p50: %d\n", p50)
		fmt.Fprintf(w, "- p95: %d\n", p95)
		fmt.Fprintf(w, "- p99: %d\n", p99)
		fmt.Fprintf(w, "- max: %d\n", mx)
		fmt.Fprintf(w, "- sample size: %d RRs matched\n", len(xs))
		fmt.Fprintln(w)
	}
} else {
	p50, p95, p99, mx := percentiles(s.ReconcilesPerRR)
	fmt.Fprintf(w, "- p50: %d\n", p50)
	fmt.Fprintf(w, "- p95: %d\n", p95)
	fmt.Fprintf(w, "- p99: %d\n", p99)
	fmt.Fprintf(w, "- max: %d\n", mx)
	fmt.Fprintf(w, "- sample size: %d RRs matched\n", len(s.ReconcilesPerRR))
	fmt.Fprintln(w)
}
```

- [ ] **Step 1.5: Run existing build + lint to verify no regression**

```bash
go build -tags=perf ./test/perf/...
./bin/golangci-lint run --config=.golangci-required.yml ./test/perf/...
```

Expected: both exit 0.

- [ ] **Step 1.6: Commit**

```bash
git add test/perf/reduce/reduce.go
git commit -m "test(perf): add RRNamePatterns multi-pattern reducer support

Additive — when RRNamePatterns is empty the reducer behaves exactly as
before. Cascade perf scenario uses this to emit one percentile section
per RR kind."
```

---

## Task 2: Driver — `ApplyPromises` (multi-doc YAML)

**Files:**
- Modify: `test/perf/driver/promise.go` (append new function)

- [ ] **Step 2.1: Add `ApplyPromises` to `promise.go`**

Append to `test/perf/driver/promise.go` (after the existing `ApplyPromise`
function — keep the existing one untouched):

```go
// ApplyPromises reads a multi-doc YAML file containing one or more Promises
// separated by "---" boundaries. Each document is created (or updated if it
// already exists). The function blocks until every Promise reports
// Available=True or the per-Promise timeout fires.
//
// The driver intentionally does not parallelise the Promise applies — kratix
// expects Promises to install one at a time and the rig only applies 2-3 of
// them total at suite start.
func ApplyPromises(ctx context.Context, c client.Client, path string) ([]*platformv1alpha1.Promise, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read promises yaml: %w", err)
	}

	docs := splitYAMLDocuments(raw)
	out := make([]*platformv1alpha1.Promise, 0, len(docs))
	for i, doc := range docs {
		p := &platformv1alpha1.Promise{}
		if err := yaml.Unmarshal(doc, p); err != nil {
			return nil, fmt.Errorf("unmarshal promise doc %d: %w", i, err)
		}
		if p.GetName() == "" {
			// skip empty/whitespace-only doc fragments at the file boundary
			continue
		}

		existing := &platformv1alpha1.Promise{}
		err := c.Get(ctx, types.NamespacedName{Name: p.GetName()}, existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, p); err != nil {
				return nil, fmt.Errorf("create promise %s: %w", p.GetName(), err)
			}
		case err != nil:
			return nil, fmt.Errorf("get existing promise %s: %w", p.GetName(), err)
		default:
			p.SetResourceVersion(existing.GetResourceVersion())
			if err := c.Update(ctx, p); err != nil {
				return nil, fmt.Errorf("update promise %s: %w", p.GetName(), err)
			}
		}

		if err := WaitForPromiseAvailable(ctx, c, p.GetName(), 5*time.Minute); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// splitYAMLDocuments splits a multi-doc YAML byte slice on "---" boundaries.
// It tolerates a leading "---", trailing whitespace, and CRLF line endings.
func splitYAMLDocuments(raw []byte) [][]byte {
	const sep = "\n---"
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// ensure first doc has a preceding newline so the split treats it uniformly
	if !strings.HasPrefix(text, sep[1:]) {
		text = "\n" + text
	}
	parts := strings.Split(text, sep)
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		out = append(out, []byte(trimmed))
	}
	return out
}
```

Note: this requires `strings` in the imports. Update the import block at the
top of `promise.go` to include `"strings"`.

- [ ] **Step 2.2: Build to verify**

```bash
go build -tags=perf ./test/perf/...
```

Expected: exit 0.

- [ ] **Step 2.3: Commit**

```bash
git add test/perf/driver/promise.go
git commit -m "test(perf): add driver.ApplyPromises for multi-Promise scenarios

Reads a multi-doc YAML, applies each Promise, waits for Available=True
on every one before returning. Existing single-Promise ApplyPromise is
untouched."
```

---

## Task 3: Driver — `WaitForCascadeConverged`

**Files:**
- Modify: `test/perf/driver/convergence.go` (append)

- [ ] **Step 3.1: Add `WaitForCascadeConverged` and supporting helper**

Append to `test/perf/driver/convergence.go` (keep `WaitForReconciled` etc.
untouched):

```go
// WaitForCascadeConverged polls the leafGVK namespace listing every 2s.
// It considers the cascade converged when, for every name in sourceNames,
// there exists a leaf RR labelled labelKey=<sourceName> whose status carries
// Reconciled=True.
//
// Cascade scenarios use this when an upstream Promise's pipeline schedules a
// downstream Promise's RR mid-workflow: the upstream may flip Reconciled
// before the downstream RR even exists, so waiting on the upstream is not a
// sufficient end-condition.
func WaitForCascadeConverged(
	ctx context.Context,
	c client.Client,
	leafGVK schema.GroupVersionKind,
	leafNamespace string,
	labelKey string,
	sourceNames []string,
	timeout time.Duration,
	report func(Progress),
) error {
	wantSet := make(map[string]struct{}, len(sourceNames))
	for _, n := range sourceNames {
		wantSet[n] = struct{}{}
	}
	start := time.Now()
	deadline := start.Add(timeout)
	period := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		done, err := countCascadeReconciled(ctx, c, leafGVK, leafNamespace, labelKey, wantSet)
		if err != nil {
			return err
		}
		if report != nil {
			report(Progress{Total: len(sourceNames), Reconciled: done, Elapsed: time.Since(start)})
		}
		if done >= len(sourceNames) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cascade convergence timed out: %d/%d reconciled after %s",
				done, len(sourceNames), time.Since(start).Truncate(time.Second))
		}
		time.Sleep(period)
	}
}

func countCascadeReconciled(
	ctx context.Context,
	c client.Client,
	leafGVK schema.GroupVersionKind,
	leafNamespace string,
	labelKey string,
	want map[string]struct{},
) (int, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(leafGVK)
	if err := c.List(ctx, list, client.InNamespace(leafNamespace)); err != nil {
		return 0, fmt.Errorf("list leaf rrs: %w", err)
	}
	seen := make(map[string]bool, len(want))
	for i := range list.Items {
		obj := &list.Items[i]
		src := obj.GetLabels()[labelKey]
		if src == "" {
			continue
		}
		if _, ok := want[src]; !ok {
			continue
		}
		if isReconciledTrue(obj) {
			seen[src] = true
		}
	}
	return len(seen), nil
}
```

- [ ] **Step 3.2: Build to verify**

```bash
go build -tags=perf ./test/perf/...
```

Expected: exit 0.

- [ ] **Step 3.3: Commit**

```bash
git add test/perf/driver/convergence.go
git commit -m "test(perf): add WaitForCascadeConverged for multi-Promise flows

Waits on a downstream RR's Reconciled=True status, looked up via a
cascade.kratix.io/source label set by the upstream Promise's pipeline.
Used by the cascade scenario where upstream Reconciled is not a
sufficient end-condition."
```

---

## Task 4: Cascade Promises asset YAML

**Files:**
- Create: `test/perf/assets/cascade-promises.yaml`

- [ ] **Step 4.1: Write the multi-Promise YAML**

Create `test/perf/assets/cascade-promises.yaml`:

```yaml
apiVersion: platform.kratix.io/v1alpha1
kind: Promise
metadata:
  name: simpleapp
spec:
  destinationSelectors:
    - matchLabels:
        environment: dev
  api:
    apiVersion: apiextensions.k8s.io/v1
    kind: CustomResourceDefinition
    metadata:
      name: simpleapps.simpleapp.kratix.io
    spec:
      group: simpleapp.kratix.io
      names:
        kind: SimpleApp
        plural: simpleapps
        singular: simpleapp
      scope: Namespaced
      versions:
        - name: v1alpha1
          served: true
          storage: true
          schema:
            openAPIV3Schema:
              type: object
              properties:
                spec:
                  type: object
                  properties:
                    repo:
                      type: string
  workflows:
    resource:
      configure:
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: build-via-github
          spec:
            containers:
              - name: build
                image: busybox:1.36
                env:
                  - name: BUILD_DURATION_SECONDS
                    value: "0"
                command: ["sh", "-c"]
                args:
                  - |
                    SECS="${BUILD_DURATION_SECONDS:-0}"
                    i=0
                    while [ $i -lt $SECS ]; do sleep 1; i=$((i+1)); done
                    TAG=$(head -c4 /dev/urandom 2>/dev/null | xxd -p 2>/dev/null || echo "abc12345")
                    mkdir -p /kratix/metadata
                    cat > /kratix/metadata/status.yaml <<EOF
                    imageTag: sha-$TAG
                    buildUrl: http://fake-gh/build/$KRATIX_OBJECT_NAME
                    EOF
                    echo "build complete"
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: schedule-aaas
          spec:
            containers:
              - name: schedule
                image: busybox:1.36
                command: ["sh", "-c"]
                args:
                  - |
                    TAG=$(grep '^imageTag:' /kratix/metadata/status.yaml 2>/dev/null | awk '{print $2}')
                    TAG=${TAG:-unknown}
                    mkdir -p /kratix/output
                    cat > /kratix/output/aaas.yaml <<EOF
                    apiVersion: aaas.kratix.io/v1alpha1
                    kind: AppAsAService
                    metadata:
                      name: $KRATIX_OBJECT_NAME
                      namespace: $KRATIX_OBJECT_NAMESPACE
                      labels:
                        cascade.kratix.io/source: $KRATIX_OBJECT_NAME
                    spec:
                      imageTag: $TAG
                    EOF
                    echo "aaas scheduled"
---
apiVersion: platform.kratix.io/v1alpha1
kind: Promise
metadata:
  name: appasaservice
spec:
  destinationSelectors:
    - matchLabels:
        environment: dev
  api:
    apiVersion: apiextensions.k8s.io/v1
    kind: CustomResourceDefinition
    metadata:
      name: appasaservices.aaas.kratix.io
    spec:
      group: aaas.kratix.io
      names:
        kind: AppAsAService
        plural: appasaservices
        singular: appasaservice
      scope: Namespaced
      versions:
        - name: v1alpha1
          served: true
          storage: true
          schema:
            openAPIV3Schema:
              type: object
              properties:
                spec:
                  type: object
                  properties:
                    imageTag:
                      type: string
  workflows:
    resource:
      configure:
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: schedule-evopub
          spec:
            containers:
              - name: schedule
                image: busybox:1.36
                command: ["sh", "-c"]
                args:
                  - |
                    SOURCE="${KRATIX_OBJECT_NAME}"
                    mkdir -p /kratix/output
                    cat > /kratix/output/evopub.yaml <<EOF
                    apiVersion: evopub.kratix.io/v1alpha1
                    kind: EvoPublisher
                    metadata:
                      name: $SOURCE
                      namespace: $KRATIX_OBJECT_NAMESPACE
                      labels:
                        cascade.kratix.io/source: $SOURCE
                    spec:
                      appName: $SOURCE
                    EOF
                    echo "evopub scheduled"
---
apiVersion: platform.kratix.io/v1alpha1
kind: Promise
metadata:
  name: evopublisher
spec:
  destinationSelectors:
    - matchLabels:
        environment: dev
  api:
    apiVersion: apiextensions.k8s.io/v1
    kind: CustomResourceDefinition
    metadata:
      name: evopublishers.evopub.kratix.io
    spec:
      group: evopub.kratix.io
      names:
        kind: EvoPublisher
        plural: evopublishers
        singular: evopublisher
      scope: Namespaced
      versions:
        - name: v1alpha1
          served: true
          storage: true
          schema:
            openAPIV3Schema:
              type: object
              properties:
                spec:
                  type: object
                  properties:
                    appName:
                      type: string
  workflows:
    resource:
      configure:
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: publish
          spec:
            containers:
              - name: publish
                image: busybox:1.36
                env:
                  - name: PUBLISH_DURATION_SECONDS
                    value: "0"
                command: ["sh", "-c"]
                args:
                  - |
                    SECS="${PUBLISH_DURATION_SECONDS:-0}"
                    i=0
                    while [ $i -lt $SECS ]; do sleep 1; i=$((i+1)); done
                    echo "published $KRATIX_OBJECT_NAME"
```

- [ ] **Step 4.2: Lint the YAML for obvious issues**

```bash
kubectl --dry-run=client apply -f test/perf/assets/cascade-promises.yaml 2>&1 | head -10
```

Expected: 3 lines `promise.platform.kratix.io/<name> created (dry run)`.
If any "error validating data" appears, fix the schema.

- [ ] **Step 4.3: Commit**

```bash
git add test/perf/assets/cascade-promises.yaml
git commit -m "test(perf): add 3-Promise cascade fixture

SimpleApp.pipeline-1 mocks a GitHub build (sleep N), writes a status with
a fake imageTag. SimpleApp.pipeline-2 reads that status and schedules an
AppAsAService RR with the cascade.kratix.io/source label. AppAsAService
schedules an EvoPublisher; EvoPublisher's pipeline mocks the publisher
call as a sleep."
```

---

## Task 5: Cascade test file

**Files:**
- Create: `test/perf/cascade_test.go`
- Modify: `test/perf/perf_test.go:28` (existing Describe block — add `Label`)

- [ ] **Step 5.1: Label the existing no-op spec**

Find `var _ = Describe("perf rig", func() {` in `test/perf/perf_test.go:28`.
Inside, find `It("drives N resource requests...` (around line 29) and update
to add a `Label`:

```go
It("drives N resource requests to Reconciled=True and captures controller metrics",
   Label("noop"), func() {
```

Note: `Label` is a Ginkgo identifier; the existing import `. "github.com/onsi/ginkgo/v2"`
already exposes it.

- [ ] **Step 5.2: Write `cascade_test.go`**

Create `test/perf/cascade_test.go`:

```go
//go:build perf

package perf_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/syntasso/kratix/test/perf/driver"
	"github.com/syntasso/kratix/test/perf/reduce"
	"github.com/syntasso/kratix/test/perf/scraper"
)

const cascadeLabel = "cascade.kratix.io/source"

var (
	simpleAppGVK = schema.GroupVersionKind{
		Group: "simpleapp.kratix.io", Version: "v1alpha1", Kind: "SimpleApp",
	}
	evoPubGVK = schema.GroupVersionKind{
		Group: "evopub.kratix.io", Version: "v1alpha1", Kind: "EvoPublisher",
	}
)

var _ = Describe("perf rig - cascade", Label("cascade"), func() {
	It("drives N SimpleApp RRs through the 3-Promise cascade", func() {
		runDir, err := filepath.Abs(filepath.Join("results", *flagRunName))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(runDir)).To(Succeed())
		Expect(os.MkdirAll(runDir, 0o755)).To(Succeed())

		timeout, err := time.ParseDuration(*flagTimeout)
		Expect(err).NotTo(HaveOccurred())

		ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Minute)
		DeferCleanup(cancel)

		c, err := driver.NewClient(*flagContext)
		Expect(err).NotTo(HaveOccurred(), "build kube client")

		By("starting controller log tail → controller.log")
		logCmd, logFile := startLogTail(*flagContext, runDir)
		DeferCleanup(func() {
			_ = logCmd.Process.Kill()
			_ = logFile.Close()
		})

		By("provisioning metrics scraper RBAC + bearer token")
		token, err := driver.EnsureMetricsScraperRBAC(ctx, *flagContext, "kratix-platform-system", timeout+30*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("starting /metrics scraper (https + bearer)")
		sc := scraper.NewSecure(*flagContext, "kratix-platform-system", "kratix-platform-controller-manager", 8443, runDir, token)
		Expect(sc.Start(ctx)).To(Succeed())
		DeferCleanup(func() {
			if err := sc.Stop(); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "scraper stop error: %v\n", err)
			}
		})

		By("applying 3-Promise cascade fixture")
		promises, err := driver.ApplyPromises(ctx, c, "assets/cascade-promises.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(promises).To(HaveLen(3))

		DeferCleanup(func() {
			cleanupCtx, cancelClean := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancelClean()
			// Delete source RRs first; the cascade label means deleting them
			// cascades to children via the kratix work-cleanup finalizer.
			_ = driver.DeleteAll(cleanupCtx, c, driver.RequestSpec{
				GVK: simpleAppGVK, Namespace: *flagNamespace, Parallel: 50,
			})
			// Then delete the Promises in reverse dependency order so each
			// has a chance to finalise its own RRs cleanly.
			_ = driver.DeletePromise(cleanupCtx, c, "evopublisher", 10*time.Minute)
			_ = driver.DeletePromise(cleanupCtx, c, "appasaservice", 10*time.Minute)
			_ = driver.DeletePromise(cleanupCtx, c, "simpleapp", 10*time.Minute)
		})

		Expect(driver.EnsureNamespace(ctx, c, *flagNamespace)).To(Succeed())

		spec := driver.RequestSpec{
			GVK:       simpleAppGVK,
			Namespace: *flagNamespace,
			BaseName:  *flagBaseName,
			Count:     *flagN,
			Parallel:  50,
		}

		By(fmt.Sprintf("creating %d SimpleApp RRs in namespace %s", spec.Count, spec.Namespace))
		t0 := time.Now()
		names, err := driver.CreateAll(ctx, c, spec)
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "creation took %s\n", time.Since(t0).Truncate(time.Millisecond))

		By("waiting for every leaf EvoPublisher RR to reach Reconciled=True")
		convergeStart := time.Now()
		err = driver.WaitForCascadeConverged(ctx, c, evoPubGVK, spec.Namespace,
			cascadeLabel, names, timeout,
			func(p driver.Progress) {
				_, _ = fmt.Fprintf(GinkgoWriter, "  %s: %d/%d cascade-reconciled (elapsed %s)\n",
					*flagRunName, p.Reconciled, p.Total, p.Elapsed.Truncate(time.Second))
			})
		wall := time.Since(convergeStart)
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "cascade converged in %s\n", wall.Truncate(time.Second))

		By("stopping scraper and reducing artefacts → summary.md")
		if err := sc.Stop(); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "scraper accumulated errors (non-fatal): %v\n", err)
		}

		summary, err := reduce.Reduce(runDir, reduce.RunMeta{
			Name:      *flagRunName,
			N:         spec.Count,
			Namespace: spec.Namespace,
			RRNamePatterns: []reduce.NamedPattern{
				{Label: "simpleapp", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName)},
				{Label: "appasaservice", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName)},
				{Label: "evopublisher", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName)},
			},
			WallClock: wall,
		})
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "wrote %s\n", filepath.Join(runDir, "summary.md"))
		_ = summary
	})
})
```

Note: all three RR kinds end up named `<flagBaseName>-NNNNN` because both
intermediate pipelines reuse `KRATIX_OBJECT_NAME` verbatim. The reducer's
log scanner matches on RR name regex; the per-kind separation comes from
the controller log line's `logger: controllers.Promise.<promiseName>` field,
which `reconcilesPerRR` does not yet filter on. **Therefore the 3 sections
will currently show identical counts.** This is a known limitation and is
addressed in Task 7.

- [ ] **Step 5.3: Build to verify both specs compile**

```bash
go build -tags=perf ./test/perf/...
```

Expected: exit 0.

- [ ] **Step 5.4: Lint**

```bash
./bin/golangci-lint run --config=.golangci-required.yml ./test/perf/...
```

Expected: 0 issues.

- [ ] **Step 5.5: Commit**

```bash
git add test/perf/perf_test.go test/perf/cascade_test.go
git commit -m "test(perf): add cascade Ginkgo spec

Drives N SimpleApp RRs through a SimpleApp → AppAsAService → EvoPublisher
chain and waits for every leaf RR to flip Reconciled=True. Reuses the
existing scraper, metrics-auth, and log-tail primitives. Labelled
\"cascade\"; existing no-op spec labelled \"noop\"."
```

---

## Task 6: Makefile + README

**Files:**
- Modify: `Makefile:250-258` (existing `perf-test` target)
- Modify: `test/perf/README.md` (append a section)

- [ ] **Step 6.1: Update `perf-test` and add `perf-test-cascade`**

Replace the existing `perf-test` block (Makefile:250-258) with:

```make
.PHONY: perf-test
perf-test: fmt vet ## Run the no-op controller perf rig (PERF_N=2500 PERF_RUN=baseline-2500 make perf-test)
	PERF_N=$${PERF_N:-500} PERF_RUN=$${PERF_RUN:-run-$$(date +%s)} \
	go test -tags=perf -timeout=60m ./test/perf/... \
		-args -perf.n=$${PERF_N} -perf.run=$${PERF_RUN} \
		-perf.context=$${PERF_CONTEXT:-kind-platform} \
		-perf.namespace=$${PERF_NAMESPACE:-default} \
		-perf.timeout=$${PERF_TIMEOUT:-30m} \
		-perf.basename=$${PERF_BASENAME:-perftest} \
		-ginkgo.label-filter=noop

.PHONY: perf-test-cascade
perf-test-cascade: fmt vet ## Run the 3-Promise cascade perf scenario (PERF_N=100 PERF_RUN=cascade-100 make perf-test-cascade)
	PERF_N=$${PERF_N:-100} PERF_RUN=$${PERF_RUN:-run-$$(date +%s)} \
	go test -tags=perf -timeout=60m ./test/perf/... \
		-args -perf.n=$${PERF_N} -perf.run=$${PERF_RUN} \
		-perf.context=$${PERF_CONTEXT:-kind-platform} \
		-perf.namespace=$${PERF_NAMESPACE:-default} \
		-perf.timeout=$${PERF_TIMEOUT:-30m} \
		-perf.basename=$${PERF_BASENAME:-simpleapp} \
		-ginkgo.label-filter=cascade
```

- [ ] **Step 6.2: Document the cascade scenario in `test/perf/README.md`**

Append to `test/perf/README.md`:

```markdown
## Cascade scenario

`make perf-test-cascade` drives N `SimpleApp` resource requests through a
3-Promise cascade:

```
SimpleApp ──pipeline-2──▶ AppAsAService ──pipeline-1──▶ EvoPublisher
```

All pipelines are bash mocks. SimpleApp's `build-via-github` pipeline sleeps
for `BUILD_DURATION_SECONDS` (default 0, env var on the pipeline container)
and writes a fake build status. `schedule-aaas` reads that status and emits
an AppAsAService RR. `schedule-evopub` emits an EvoPublisher RR. `publish`
sleeps for `PUBLISH_DURATION_SECONDS` (default 0).

Convergence is observed at the leaf — the driver waits for every
EvoPublisher RR to reach `Reconciled=True`, found by the
`cascade.kratix.io/source` label propagated through every intermediate
manifest. Source RR's `Reconciled=True` is *not* a sufficient signal: a
SimpleApp's workflow flips Reconciled as soon as its two pipelines finish,
which is well before the downstream RRs exist.

`summary.md` shows one "Reconciles per RR" subsection per Promise kind.
Per-controller workqueue/longest-running peaks include all three dynamic-RR
controllers.

The asset is `assets/cascade-promises.yaml`. Edit the env vars on the
pipeline containers if you want to model slow CI or slow publish.
```

- [ ] **Step 6.3: Verify `make perf-test --dry-run` syntax**

```bash
make -n perf-test 2>&1 | head -5
make -n perf-test-cascade 2>&1 | head -5
```

Expected: both print the `go test` invocation including the new label filter.

- [ ] **Step 6.4: Commit**

```bash
git add Makefile test/perf/README.md
git commit -m "test(perf): add perf-test-cascade make target + README

Existing perf-test now filters Ginkgo label \"noop\" so it doesn't run
the cascade spec by default. perf-test-cascade filters \"cascade\"."
```

---

## Task 7: Differentiate per-kind reconcile counts in cascade summary

**Why this is its own task**: as flagged in Task 5.2, all three RR kinds
share a name pattern (`simpleapp-NNNNN`), so the regex-only log scanner
counts them identically. The reducer needs to also key on the controller
identifier in the log line.

**Files:**
- Modify: `test/perf/reduce/reduce.go` (extend `reconcilesPerRR`)

- [ ] **Step 7.1: Add controller filter to log scanner**

Find `reconcilesPerRR(logPath, rrPattern string) ([]int, error)` (currently
`reduce.go:77`). Replace its signature and body to accept an optional
controller-name filter:

```go
// reconcilesPerRR counts reconciliation-started log lines matching rrPattern,
// optionally restricted to lines whose JSON includes
// "logger":"controllers.Promise.<controller>". Empty controller name disables
// the filter (legacy single-Promise behaviour).
func reconcilesPerRR(logPath, rrPattern, controller string) ([]int, error) {
	rrRE, err := regexp.Compile(rrPattern)
	if err != nil {
		return nil, fmt.Errorf("rr name pattern %q: %w", rrPattern, err)
	}
	var controllerRE *regexp.Regexp
	if controller != "" {
		controllerRE = regexp.MustCompile(
			fmt.Sprintf(`"logger":"[^"]*controllers\.Promise\.%s"`, regexp.QuoteMeta(controller)),
		)
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !reconStartRE.MatchString(line) {
			continue
		}
		if controllerRE != nil && !controllerRE.MatchString(line) {
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
```

- [ ] **Step 7.2: Extend `NamedPattern` with a `Controller` field**

In `RunMeta`'s definition (added in Task 1), update `NamedPattern`:

```go
type NamedPattern struct {
	Label      string
	Pattern    string
	Controller string // controller-runtime controller name, e.g. "simpleapp"
}
```

- [ ] **Step 7.3: Pass through in `Reduce`**

Update the two `reconcilesPerRR` call sites in `Reduce` (added in Task 1
step 1.3) to pass the controller:

```go
// multi-pattern path
rcs, err := reconcilesPerRR(logPath, np.Pattern, np.Controller)
// single-pattern path
rcs, err := reconcilesPerRR(logPath, meta.RRNamePattern, "")
```

- [ ] **Step 7.4: Populate `Controller` in the cascade test**

Find the `RRNamePatterns` slice in `test/perf/cascade_test.go` (Task 5.2,
inside the `reduce.Reduce` call). Replace with:

```go
RRNamePatterns: []reduce.NamedPattern{
	{Label: "simpleapp", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName), Controller: "simpleapp"},
	{Label: "appasaservice", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName), Controller: "appasaservice"},
	{Label: "evopublisher", Pattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName), Controller: "evopublisher"},
},
```

- [ ] **Step 7.5: Build + lint**

```bash
go build -tags=perf ./test/perf/...
./bin/golangci-lint run --config=.golangci-required.yml ./test/perf/...
```

Expected: exit 0 / 0 issues.

- [ ] **Step 7.6: Commit**

```bash
git add test/perf/reduce/reduce.go test/perf/cascade_test.go
git commit -m "test(perf): filter reconcile log scan by controller name

Cascade scenario has three RR kinds with identical name patterns; the
controller-name filter on the log line's logger field disambiguates them
so summary.md shows correct per-kind percentiles."
```

---

## Task 8: End-to-end validation on the running cluster

**Files:** none modified — this is a verification step.

- [ ] **Step 8.1: Recreate kind cluster for clean state**

```bash
RECREATE=true make quick-start
```

Expected: ends with `kratix .* installed` and no errors. Takes ~3 minutes.

- [ ] **Step 8.2: Run cascade at N=10 first as a smoke test**

```bash
PERF_N=10 PERF_RUN=cascade-smoke-10 PERF_TIMEOUT=10m make perf-test-cascade
```

Expected: ends with `cascade converged in <duration>` and writes
`test/perf/results/cascade-smoke-10/summary.md` containing three
`### simpleapp` / `### appasaservice` / `### evopublisher` subsections, each
with `sample size: 10 RRs matched`.

- [ ] **Step 8.3: Confirm the cascade actually fired**

```bash
kubectl --context=kind-platform get simpleapps,appasaservices,evopublishers -A 2>&1 | head -40
kubectl --context=kind-platform get evopublishers -l cascade.kratix.io/source -A 2>&1 | head -5
```

Expected: 10 of each kind, all in `Reconciled` / `Available` state. The
labelled list returns 10 items.

- [ ] **Step 8.4: Run cascade at N=100 against the best controller config**

```bash
./test/perf/set-controller-flags.sh '["--metrics-bind-address=:8443","--health-probe-bind-address=:8081","--leader-elect","--dynamic-rr-max-concurrent-reconciles=20","--kube-api-qps=200","--kube-api-burst=400","--dynamic-rr-filter-noop-writes=true"]'
PERF_N=100 PERF_RUN=cascade-100-all PERF_TIMEOUT=15m make perf-test-cascade
```

Expected: convergence within ~3-5 minutes for N=100 (will be empirically
established here for the first time; if it takes > 15m investigate before
declaring success).

- [ ] **Step 8.5: Verify the existing no-op test still passes**

```bash
PERF_N=10 PERF_RUN=noop-smoke-10 PERF_TIMEOUT=10m make perf-test
```

Expected: existing behaviour — single "Reconciles per RR" section, no
cascade subsections. Confirms Task 1's additive change didn't regress.

- [ ] **Step 8.6: Commit any documentation refinements that emerged**

If the smoke runs surfaced anything worth noting (e.g. unexpected timing,
a finalizer ordering issue), update `test/perf/README.md` with a one-line
note. Otherwise, no commit needed at this step.

---

## Self-review

**Spec coverage:**
- Workload shape ✓ Task 4 (asset YAML).
- ApplyPromises driver helper ✓ Task 2.
- WaitForCascadeConverged driver helper ✓ Task 3.
- Reducer multi-pattern ✓ Task 1 + Task 7 (per-controller filter).
- Cascade test file ✓ Task 5.
- Label-based Ginkgo disambiguation ✓ Task 5 + Task 6.
- Makefile target ✓ Task 6.
- README paragraph ✓ Task 6.
- Test plan from spec ✓ Task 8.

**Placeholder scan:** none. Every step has concrete code or commands.

**Type consistency:**
- `NamedPattern` introduced with `{Label, Pattern}` in Task 1, extended with
  `Controller` in Task 7 — Task 7 explicitly shows the updated struct.
- `WaitForCascadeConverged` signature is identical between Task 3 and the
  call site in Task 5.
- `RRNamePatterns []NamedPattern` field added in Task 1, used in Task 5 with
  3 elements, updated in Task 7 to populate `Controller`. ✓

**Scope check:** 8 tasks, ~3-4 hours total. Single-PR-sized. No
cross-cutting decisions deferred.

No issues to fix.
