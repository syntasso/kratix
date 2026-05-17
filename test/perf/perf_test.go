//go:build perf

package perf_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/test/perf/driver"
	"github.com/syntasso/kratix/test/perf/reduce"
	"github.com/syntasso/kratix/test/perf/scraper"
)

var (
	flagRRGroup   = flag.String("perf.rr.group", "perf.kratix.io", "API group of the resource request CRD")
	flagRRVersion = flag.String("perf.rr.version", "v1alpha1", "API version of the resource request CRD")
	flagRRKind    = flag.String("perf.rr.kind", "PerfTest", "Kind of the resource request CRD")
)

func rrGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   *flagRRGroup,
		Version: *flagRRVersion,
		Kind:    *flagRRKind,
	}
}

var _ = Describe("perf rig", func() {
	It("drives N resource requests to Reconciled=True and captures controller metrics", func() {
		runDir, err := filepath.Abs(filepath.Join("results", *flagRunName))
		Expect(err).NotTo(HaveOccurred())
		// Wipe any prior artefacts for this run name so stale metric snapshots
		// from earlier attempts don't pollute the reduced summary.
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

		var sc *scraper.Scraper
		if !*flagNoScrape {
			By("provisioning metrics scraper RBAC + bearer token")
			token, err := driver.EnsureMetricsScraperRBAC(ctx, *flagContext, "kratix-platform-system", timeout+30*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("starting /metrics scraper (https + bearer)")
			sc = scraper.NewSecure(*flagContext, "kratix-platform-system", "kratix-platform-controller-manager", 8443, runDir, token)
			Expect(sc.Start(ctx)).To(Succeed())
			DeferCleanup(func() {
				if err := sc.Stop(); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "scraper stop error: %v\n", err)
				}
			})
		} else {
			By("skipping /metrics scraper (--perf.no-scrape)")
		}

		Expect(driver.EnsureNamespace(ctx, c, *flagNamespace)).To(Succeed())

		if *flagPromises <= 1 {
			runSinglePromise(ctx, c, runDir, sc, timeout)
			return
		}
		runMultiPromise(ctx, c, runDir, sc, timeout)
	})
})

func runSinglePromise(ctx context.Context, c client.Client, runDir string, sc *scraper.Scraper, timeout time.Duration) {
	By("applying Promise")
	_, err := driver.ApplyPromise(ctx, c, *flagPromise)
	Expect(err).NotTo(HaveOccurred())
	if !*flagKeep {
		DeferCleanup(func() {
			cleanupCtx, cancelClean := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancelClean()
			_ = driver.DeleteAll(cleanupCtx, c, driver.RequestSpec{
				GVK:       rrGVK(),
				Namespace: *flagNamespace,
				Parallel:  100,
			})
			_ = driver.DeletePromise(cleanupCtx, c, *flagPromiseName, 10*time.Minute)
		})
	} else {
		DeferCleanup(func() {
			_, _ = fmt.Fprintf(GinkgoWriter, "--perf.keep=true: leaving Promise %q and resource requests in place\n", *flagPromiseName)
		})
	}

	spec := driver.RequestSpec{
		GVK:       rrGVK(),
		Namespace: *flagNamespace,
		BaseName:  *flagBaseName,
		Count:     *flagN,
		Parallel:  50,
	}

	By(fmt.Sprintf("creating %d resource requests in namespace %s", spec.Count, spec.Namespace))
	t0 := time.Now()
	names, err := driver.CreateAll(ctx, c, spec)
	Expect(err).NotTo(HaveOccurred())
	_, _ = fmt.Fprintf(GinkgoWriter, "creation took %s\n", time.Since(t0).Truncate(time.Millisecond))

	By("waiting for every RR to reach Reconciled=True")
	convergeStart := time.Now()
	err = driver.WaitForReconciled(ctx, c, rrGVK(), spec.Namespace, names, timeout,
		func(p driver.Progress) {
			_, _ = fmt.Fprintf(GinkgoWriter, "  %s: %d/%d reconciled (elapsed %s)\n",
				*flagRunName, p.Reconciled, p.Total, p.Elapsed.Truncate(time.Second))
		})
	wall := time.Since(convergeStart)
	Expect(err).NotTo(HaveOccurred())
	_, _ = fmt.Fprintf(GinkgoWriter, "converged in %s\n", wall.Truncate(time.Second))

	waitForDownstream(ctx, c, *flagN)

	if sc != nil {
		By("stopping scraper and reducing artefacts → summary.md")
		if err := sc.Stop(); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "scraper accumulated errors (non-fatal): %v\n", err)
		}
		summary, err := reduce.Reduce(runDir, reduce.RunMeta{
			Name:          *flagRunName,
			N:             spec.Count,
			Namespace:     spec.Namespace,
			RRNamePattern: fmt.Sprintf(`%s-\d{5}`, *flagBaseName),
			WallClock:     wall,
		})
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "wrote %s\n", filepath.Join(runDir, "summary.md"))
		_ = summary
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "%s: converged in %s (no metrics summary; --perf.no-scrape)\n", *flagRunName, wall.Truncate(time.Second))
	}
}

func runMultiPromise(ctx context.Context, c client.Client, runDir string, sc *scraper.Scraper, timeout time.Duration) {
	By(fmt.Sprintf("materialising %d Promise variants from %s", *flagPromises, *flagPromise))
	tmpl, err := driver.LoadPromiseTemplate(*flagPromise)
	Expect(err).NotTo(HaveOccurred())

	promises := make([]*platformv1alpha1.Promise, *flagPromises)
	gvks := make([]driver.GVKInfo, *flagPromises)
	for i := 0; i < *flagPromises; i++ {
		p, info, err := tmpl.Materialise(i)
		Expect(err).NotTo(HaveOccurred())
		promises[i] = p
		gvks[i] = *info
	}

	By(fmt.Sprintf("applying %d Promises in parallel", *flagPromises))
	var applyWg sync.WaitGroup
	applyErrs := make([]error, *flagPromises)
	for i := range promises {
		applyWg.Add(1)
		go func(idx int) {
			defer applyWg.Done()
			applyErrs[idx] = driver.ApplyPromiseObject(ctx, c, promises[idx])
		}(i)
	}
	applyWg.Wait()
	for i, err := range applyErrs {
		Expect(err).NotTo(HaveOccurred(), "apply promise %s", gvks[i].PromiseName)
	}

	if !*flagKeep {
		DeferCleanup(func() {
			cleanupCtx, cancelClean := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancelClean()
			var cwg sync.WaitGroup
			for i := range gvks {
				cwg.Add(1)
				go func(info driver.GVKInfo) {
					defer cwg.Done()
					_ = driver.DeleteAll(cleanupCtx, c, driver.RequestSpec{
						GVK:       schema.GroupVersionKind{Group: info.Group, Version: info.Version, Kind: info.Kind},
						Namespace: *flagNamespace,
						Parallel:  100,
					})
					_ = driver.DeletePromise(cleanupCtx, c, info.PromiseName, 10*time.Minute)
				}(gvks[i])
			}
			cwg.Wait()
		})
	} else {
		DeferCleanup(func() {
			names := make([]string, len(gvks))
			for i, info := range gvks {
				names[i] = info.PromiseName
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "--perf.keep=true: leaving Promises %v and resource requests in place\n", names)
		})
	}

	type promiseStats struct {
		Name          string
		Created       time.Time
		AllReconciled time.Time
	}
	stats := make([]promiseStats, *flagPromises)
	var totalCreated atomic.Int64

	By(fmt.Sprintf("driving %d RRs against each of %d Promises", *flagN, *flagPromises))
	overallStart := time.Now()
	var rwg sync.WaitGroup
	driveErrs := make([]error, *flagPromises)
	for i := range gvks {
		rwg.Add(1)
		go func(idx int, info driver.GVKInfo) {
			defer rwg.Done()
			spec := driver.RequestSpec{
				GVK:       schema.GroupVersionKind{Group: info.Group, Version: info.Version, Kind: info.Kind},
				Namespace: *flagNamespace,
				BaseName:  fmt.Sprintf("%s-%02d", *flagBaseName, idx),
				Count:     *flagN,
				Parallel:  50,
			}
			stats[idx].Name = info.PromiseName
			stats[idx].Created = time.Now()
			names, err := driver.CreateAll(ctx, c, spec)
			if err != nil {
				driveErrs[idx] = fmt.Errorf("create RRs for %s: %w", info.PromiseName, err)
				return
			}
			totalCreated.Add(int64(len(names)))
			if err := driver.WaitForReconciled(ctx, c, spec.GVK, spec.Namespace, names, timeout, nil); err != nil {
				driveErrs[idx] = fmt.Errorf("converge %s: %w", info.PromiseName, err)
				return
			}
			stats[idx].AllReconciled = time.Now()
		}(i, gvks[i])
	}
	rwg.Wait()
	overallWall := time.Since(overallStart)
	for _, err := range driveErrs {
		Expect(err).NotTo(HaveOccurred())
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "overall wall: %s for %d Promises × %d RRs (created=%d)\n",
		overallWall.Truncate(time.Second), *flagPromises, *flagN, totalCreated.Load())

	// Per-Promise duration distribution.
	durs := make([]time.Duration, len(stats))
	for i, s := range stats {
		durs[i] = s.AllReconciled.Sub(s.Created)
	}
	sorted := make([]int, len(stats))
	for i := range sorted {
		sorted[i] = i
	}
	sort.Slice(sorted, func(a, b int) bool { return durs[sorted[a]] < durs[sorted[b]] })

	pct := func(p float64) time.Duration {
		if len(sorted) == 0 {
			return 0
		}
		idx := int(float64(len(sorted)-1) * p)
		return durs[sorted[idx]]
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "per-Promise convergence: min=%s median=%s p95=%s max=%s\n",
		pct(0).Truncate(time.Second),
		pct(0.50).Truncate(time.Second),
		pct(0.95).Truncate(time.Second),
		pct(1.0).Truncate(time.Second),
	)
	for _, idx := range sorted {
		_, _ = fmt.Fprintf(GinkgoWriter, "  %s: %s\n", stats[idx].Name, durs[idx].Truncate(time.Second))
	}

	expectedDownstream := (*flagPromises) * (*flagN)
	waitForDownstream(ctx, c, expectedDownstream)

	if sc != nil {
		By("stopping scraper and reducing artefacts → summary.md")
		if err := sc.Stop(); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "scraper accumulated errors (non-fatal): %v\n", err)
		}
		summary, err := reduce.Reduce(runDir, reduce.RunMeta{
			Name:          *flagRunName,
			N:             (*flagN) * (*flagPromises),
			Namespace:     *flagNamespace,
			RRNamePattern: fmt.Sprintf(`%s-\d{2}-\d{5}`, *flagBaseName),
			WallClock:     overallWall,
		})
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "wrote %s\n", filepath.Join(runDir, "summary.md"))
		_ = summary
	}
}

// waitForDownstream polls each --perf.downstream-kind for `expected` items
// in the rig namespace. Returns immediately if no downstream kinds configured.
func waitForDownstream(ctx context.Context, c client.Client, expected int) {
	if strings.TrimSpace(*flagDownstreamKinds) == "" {
		return
	}
	if strings.TrimSpace(*flagDownstreamGroup) == "" {
		_, _ = fmt.Fprintf(GinkgoWriter, "skipping downstream watch: --perf.downstream-group is empty\n")
		return
	}
	dTimeout, err := time.ParseDuration(*flagDownstreamTimeout)
	Expect(err).NotTo(HaveOccurred(), "parse --perf.downstream-timeout")

	kinds := strings.Split(*flagDownstreamKinds, ",")
	for i := range kinds {
		kinds[i] = strings.TrimSpace(kinds[i])
	}

	By(fmt.Sprintf("waiting for downstream kinds %v in %s/%s to reach %d each",
		kinds, *flagDownstreamGroup, *flagDownstreamVersion, expected))

	for _, kind := range kinds {
		if kind == "" {
			continue
		}
		start := time.Now()
		deadline := start.Add(dTimeout)
		var got int
		for {
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   *flagDownstreamGroup,
				Version: *flagDownstreamVersion,
				Kind:    kind + "List",
			})
			err := c.List(ctx, list, client.InNamespace(*flagNamespace))
			if err == nil {
				got = len(list.Items)
				if got >= expected {
					break
				}
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "  list %s err (will retry): %v\n", kind, err)
			}
			if time.Now().After(deadline) {
				Fail(fmt.Sprintf("downstream kind %s reached only %d/%d within %s", kind, got, expected, dTimeout))
			}
			select {
			case <-ctx.Done():
				Fail(fmt.Sprintf("context cancelled waiting for downstream kind %s: %v", kind, ctx.Err()))
			case <-time.After(2 * time.Second):
			}
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "  downstream %s: %d items in %s\n",
			kind, got, time.Since(start).Truncate(time.Second))
	}
}

func startLogTail(kubeContext, runDir string) (*exec.Cmd, *os.File) {
	logPath := filepath.Join(runDir, "controller.log")
	f, err := os.Create(logPath)
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.Command("kubectl",
		"--context="+kubeContext,
		"-n", "kratix-platform-system",
		"logs",
		"deploy/kratix-platform-controller-manager",
		"--follow",
		"--tail=0",
	)
	cmd.Stdout = f
	cmd.Stderr = f
	Expect(cmd.Start()).To(Succeed())
	return cmd, f
}
