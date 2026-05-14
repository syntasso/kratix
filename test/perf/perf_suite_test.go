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
