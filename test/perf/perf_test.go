//go:build perf

package perf_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"

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

		By("applying no-op Promise")
		_, err = driver.ApplyPromise(ctx, c, *flagPromise)
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

		Expect(driver.EnsureNamespace(ctx, c, *flagNamespace)).To(Succeed())

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

		By("stopping scraper and reducing artefacts → summary.md")
		// Scraper errors are non-fatal — a transient port-forward blip
		// loses a snapshot but not the run. The reducer will fail loudly
		// later if there are no snapshots at all.
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
	})
})

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
