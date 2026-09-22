package system_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/test/kubeutils"

	"github.com/onsi/ginkgo/v2/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Kratix Healthcheck", func() {
	promiseName, resourcename := "healthchecktest", "example"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", "assets/healthchecks/promise.yaml")
		Eventually(func() string {
			return platform.Kubectl("get", "crd")
		}).Should(ContainSubstring(promiseName))
		platform.Kubectl("apply", "-f", "assets/healthchecks/resource-request.yaml")
	})

	AfterEach(func() {
		if CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.EventuallyKubectlDelete("promise", promiseName)
		}
	})

	Describe("Resource with HealthRecord", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/healthchecks/healthy-health-record.yaml")
			platform.Kubectl("apply", "-f", "assets/healthchecks/unhealthy-health-record.yaml")
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.Kubectl("delete", "-f", "assets/healthchecks/healthy-health-record.yaml")
				platform.Kubectl("delete", "-f", "assets/healthchecks/unhealthy-health-record.yaml")
				platform.EventuallyKubectlDelete(promiseName, resourcename)
				platform.EventuallyKubectlDelete("promise", promiseName)
			}
		})

		It("updates the resource status healthRecord with the HealthRecord data", func() {
			By("reporting the health state of the healthrecord", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "healthrecords", "healthy-example")
				}).Should(ContainSubstring("healthy"))

				Eventually(func() string {
					return platform.Kubectl("get", "healthrecords", "unhealthy-example")
				}).Should(ContainSubstring("unhealthy"))
			})

			By("updating the associated resource", func() {
				Eventually(func(g Gomega) {
					rawRR := platform.Kubectl("get", "-o", "json", promiseName, resourcename)
					rr := &unstructured.Unstructured{}
					g.Expect(rr.UnmarshalJSON([]byte(rawRR))).To(Succeed())

					status, _, err := unstructured.NestedMap(rr.Object, "status")
					g.Expect(err).NotTo(HaveOccurred(), "status not found")

					healthStatus, _, err := unstructured.NestedMap(status, "healthStatus")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord not found")

					g.Expect(healthStatus).To(HaveKeyWithValue("state", "unhealthy"))
					records, _, err := unstructured.NestedSlice(healthStatus, "healthRecords")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord.healthRecords not found")
					g.Expect(records).To(HaveLen(2))
				}).Should(Succeed())
			})
		})
	})
})

// Byte-for-byte what the pipelines in versioned-promise.yaml and
// unversioned-promise.yaml write to /kratix/output; the comment, key order
// and quoted "true" cannot survive a re-marshal.
func healthcheckConfigMap(promiseName string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s-config
  namespace: default
data:
  # comment the work-writer must keep
  zeta: "true"
  alpha: |
    line one
    line two
`, promiseName)
}

func healthcheckHealthDefinition(promiseName string) string {
	return fmt.Sprintf(`apiVersion: platform.kratix.io/v1alpha1
kind: HealthDefinition
metadata:
  name: %[1]s-example
  namespace: default
spec:
  promiseRef:
    name: %[1]s
  resourceRef:
    name: example
    namespace: default
  schedule: "* * * * *"
  input: ""
  workflow:
    apiVersion: platform.kratix.io/v1alpha1
    kind: Pipeline
    metadata:
      name: health
    spec:
      containers:
        - name: health
          image: ghcr.io/syntasso/kratix-pipeline-utility:v0.0.1
`, promiseName)
}

var _ = Describe("Kratix Healthcheck promise version", func() {
	const resourceName = "example"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)
	})

	// resourceWorkloads returns the decompressed resource Work content keyed by
	// filepath, failing g until the Work exists.
	resourceWorkloads := func(g Gomega, promiseName string) map[string][]byte {
		selector := strings.Join([]string{
			"kratix.io/promise-name=" + promiseName,
			"kratix.io/work-type=resource",
			"kratix.io/resource-name=" + resourceName,
			"kratix.io/pipeline-name=instance-configure",
		}, ",")
		works := &v1alpha1.WorkList{}
		kubeutils.ParseOutput(platform.KubectlG(g, "get", "works", "-n", "default", "-l", selector, "-o", "json"), works)
		g.Expect(works.Items).To(HaveLen(1))

		workloads := map[string][]byte{}
		for _, group := range works.Items[0].Spec.WorkloadGroups {
			for _, workload := range group.Workloads {
				content, err := compression.DecompressContent([]byte(workload.Content))
				g.Expect(err).NotTo(HaveOccurred())
				workloads[workload.Filepath] = content
			}
		}
		return workloads
	}

	resourceStatus := func(g Gomega, plural string) map[string]any {
		rr := &unstructured.Unstructured{}
		g.Expect(rr.UnmarshalJSON([]byte(platform.KubectlG(g, "get", "-o", "json", plural, resourceName)))).To(Succeed())
		status, _, err := unstructured.NestedMap(rr.Object, "status")
		g.Expect(err).NotTo(HaveOccurred())
		return status
	}

	configureCompleted := func(g Gomega, plural string) {
		g.Expect(platform.KubectlG(g, "get", plural, resourceName,
			`-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}`)).To(Equal("True"))
	}

	Describe("versioned Promise", func() {
		const promiseName = "healthcheckversioned"

		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/healthchecks/versioned-promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "crd")
			}).Should(ContainSubstring(promiseName))
			platform.Kubectl("apply", "-f", "assets/healthchecks/versioned-resource-request.yaml")
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.EventuallyKubectlDelete(promiseName, resourceName)
				platform.EventuallyKubectlDelete("promise", promiseName)
			}
		})

		It("stamps the Promise version on HealthDefinitions and resets healthStatus", func() {
			By("stamping spec.promiseVersion on the HealthDefinition only", func() {
				Eventually(func(g Gomega) {
					workloads := resourceWorkloads(g, promiseName)
					g.Expect(workloads).To(HaveKey("healthdefinition.yaml"))
					healthDefinition := map[string]any{}
					kubeutils.ParseOutput(string(workloads["healthdefinition.yaml"]), &healthDefinition)
					g.Expect(healthDefinition["kind"]).To(Equal("HealthDefinition"))
					promiseVersion, _, err := unstructured.NestedString(healthDefinition, "spec", "promiseVersion")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(promiseVersion).To(Equal("v2.0.0"))

					g.Expect(string(workloads["configmap.yaml"])).To(Equal(healthcheckConfigMap(promiseName)))
				}).Should(Succeed())
			})

			By("resetting status.healthStatus to unknown for the version", func() {
				Eventually(func(g Gomega) {
					g.Expect(resourceStatus(g, promiseName)).To(HaveKeyWithValue("healthStatus", map[string]any{
						"state":          "unknown",
						"promiseVersion": "v2.0.0",
					}))
				}).Should(Succeed())
			})
		})
	})

	Describe("unversioned Promise", func() {
		const promiseName = "healthcheckunversioned"

		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/healthchecks/unversioned-promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "crd")
			}).Should(ContainSubstring(promiseName))
			platform.Kubectl("apply", "-f", "assets/healthchecks/unversioned-resource-request.yaml")
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.EventuallyKubectlDelete(promiseName, resourceName)
				platform.EventuallyKubectlDelete("promise", promiseName)
			}
		})

		It("ships the pipeline output untouched and leaves healthStatus unset", func() {
			By("shipping both files byte for byte", func() {
				Eventually(func(g Gomega) {
					workloads := resourceWorkloads(g, promiseName)
					g.Expect(string(workloads["healthdefinition.yaml"])).To(Equal(healthcheckHealthDefinition(promiseName)))
					g.Expect(string(workloads["configmap.yaml"])).To(Equal(healthcheckConfigMap(promiseName)))
				}).Should(Succeed())
			})

			By("not setting status.healthStatus once the workflow completes", func() {
				Eventually(func(g Gomega) {
					configureCompleted(g, promiseName)
				}).Should(Succeed())
				Expect(resourceStatus(Default, promiseName)).NotTo(HaveKey("healthStatus"))
			})
		})
	})
})
