//go:build perf

package perf_test

import (
	"flag"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	flagN           = flag.Int("perf.n", 500, "number of resource requests to drive")
	flagRunName     = flag.String("perf.run", "run", "subdirectory under test/perf/results/ for this run's artefacts")
	flagContext     = flag.String("perf.context", "kind-platform", "kube context targeting the platform cluster")
	flagNamespace   = flag.String("perf.namespace", "default", "namespace to create RRs in")
	flagTimeout     = flag.String("perf.timeout", "30m", "convergence timeout, e.g. 30m")
	flagBaseName    = flag.String("perf.basename", "perftest", "base name for created RRs (suffixed -NNNNN)")
	flagPromise     = flag.String("perf.promise", "assets/noop-promise.yaml", "path to Promise yaml")
	flagPromiseName = flag.String("perf.promise.name", "perftest", "name of the Promise object")
	flagKeep        = flag.Bool("perf.keep", false, "skip the cleanup phase and leave the Promise + resource requests in place after the run")
	flagNoScrape    = flag.Bool("perf.no-scrape", false, "skip the /metrics scraper and summary reduction (use for concurrent rig runs)")

	flagPromises          = flag.Int("perf.promises", 1, "number of Promise copies to apply (template substitutes name + GVK by index)")
	flagDownstreamKinds   = flag.String("perf.downstream-kinds", "", "comma-separated list of additional Kinds to poll for landed counts after RRs converge (empty = skip)")
	flagDownstreamGroup   = flag.String("perf.downstream-group", "", "API group for downstream-kinds (e.g. compound.kratix.io)")
	flagDownstreamVersion = flag.String("perf.downstream-version", "v1alpha1", "API version for downstream-kinds")
	flagDownstreamTimeout = flag.String("perf.downstream-timeout", "5m", "max wait for downstream-kinds to reach N*M each")
)

func TestPerf(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kratix controller perf rig")
}

var _ = BeforeSuite(func() {
	// honour env overrides for CI-style invocation; flags take precedence.
	if v := os.Getenv("PERF_N"); v != "" && !isFlagSet("perf.n") {
		_ = flag.Set("perf.n", v)
	}
	if v := os.Getenv("PERF_KEEP"); v != "" && !isFlagSet("perf.keep") {
		_ = flag.Set("perf.keep", v)
	}
	if v := os.Getenv("PERF_PROMISES"); v != "" && !isFlagSet("perf.promises") {
		_ = flag.Set("perf.promises", v)
	}
	if v := os.Getenv("PERF_DOWNSTREAM_KINDS"); v != "" && !isFlagSet("perf.downstream-kinds") {
		_ = flag.Set("perf.downstream-kinds", v)
	}
	if v := os.Getenv("PERF_DOWNSTREAM_GROUP"); v != "" && !isFlagSet("perf.downstream-group") {
		_ = flag.Set("perf.downstream-group", v)
	}
	if v := os.Getenv("PERF_DOWNSTREAM_VERSION"); v != "" && !isFlagSet("perf.downstream-version") {
		_ = flag.Set("perf.downstream-version", v)
	}
	if v := os.Getenv("PERF_DOWNSTREAM_TIMEOUT"); v != "" && !isFlagSet("perf.downstream-timeout") {
		_ = flag.Set("perf.downstream-timeout", v)
	}
})

func isFlagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
