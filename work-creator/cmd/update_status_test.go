package cmd_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/cmd"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var _ = Describe("updateStatus", func() {
	var (
		baseDir         string
		params          *helpers.Parameters
		objectClient    dynamic.ResourceInterface
		existingVersion string
	)

	existingHealthStatus := func() map[string]any {
		return map[string]any{
			"state":                  "healthy",
			"expectedPromiseVersion": existingVersion,
			"healthRecords":          []any{map[string]any{"name": "a"}},
		}
	}

	writeFile := func(name, content string) {
		Expect(os.WriteFile(filepath.Join(baseDir, name), []byte(content), 0o600)).To(Succeed())
	}

	writeCountFile := func(healthDefinitions string) {
		writeFile(lib.HealthDefinitionCountFile, "promiseVersion: v2.0.0\nhealthDefinitions: "+healthDefinitions+"\n")
	}

	run := func() error { return cmd.UpdateStatus(context.Background(), baseDir, params, objectClient) }

	currentStatus := func() map[string]any {
		obj, err := objectClient.Get(context.Background(), "my-rr", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		status, _ := obj.Object["status"].(map[string]any)
		return status
	}

	BeforeEach(func() {
		baseDir = GinkgoT().TempDir()
		existingVersion = "v1.0.0"
		GinkgoT().Setenv(v1alpha1.KratixActionEnvVar, string(v1alpha1.WorkflowActionConfigure))

		params = &helpers.Parameters{
			ObjectGroup:     "marketplace.kratix.io",
			ObjectVersion:   "v1alpha1",
			ObjectName:      "my-rr",
			ObjectNamespace: "default",
			CRDPlural:       "databases",
			WorkflowType:    v1alpha1.WorkflowTypeResource,
			PromiseVersion:  "v2.0.0",
		}
	})

	JustBeforeEach(func() {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "marketplace.kratix.io/v1alpha1",
			"kind":       "Database",
			"metadata":   map[string]any{"name": "my-rr", "namespace": "default"},
			"status": map[string]any{
				"message":      "Pending",
				"healthStatus": existingHealthStatus(),
			},
		}}
		gvr := schema.GroupVersionResource{Group: "marketplace.kratix.io", Version: "v1alpha1", Resource: "databases"}
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{gvr: "DatabaseList"}, obj)
		objectClient = client.Resource(gvr).Namespace("default")
	})

	When("a versioned resource configure workflow shipped HealthDefinitions", func() {
		BeforeEach(func() {
			writeCountFile("2")
			writeFile("status.yaml", "message: Resource requested\nextra: value\n")
		})

		It("records the expected version and count, keeping state, records and user fields", func() {
			Expect(run()).To(Succeed())

			status := currentStatus()
			Expect(status).To(SatisfyAll(
				HaveKeyWithValue("message", "Resource requested"),
				HaveKeyWithValue("extra", "value"),
			))
			Expect(status["healthStatus"]).To(Equal(map[string]any{
				"state":                  "healthy",
				"expectedPromiseVersion": "v2.0.0",
				"healthDefinitions":      int64(2),
				"healthRecords":          []any{map[string]any{"name": "a"}},
			}))
		})

		When("the resource already expects the same promiseVersion", func() {
			BeforeEach(func() { existingVersion = "v2.0.0" })

			It("changes nothing but the count", func() {
				Expect(run()).To(Succeed())
				Expect(currentStatus()["healthStatus"]).To(Equal(map[string]any{
					"state":                  "healthy",
					"expectedPromiseVersion": "v2.0.0",
					"healthDefinitions":      int64(2),
					"healthRecords":          []any{map[string]any{"name": "a"}},
				}))
			})
		})
	})

	When("a versioned resource configure workflow shipped no HealthDefinition", func() {
		BeforeEach(func() { writeCountFile("0") })

		It("records the expected version with a count of zero", func() {
			Expect(run()).To(Succeed())
			Expect(currentStatus()["healthStatus"]).To(SatisfyAll(
				HaveKeyWithValue("expectedPromiseVersion", "v2.0.0"),
				HaveKeyWithValue("healthDefinitions", int64(0)),
				HaveKeyWithValue("state", "healthy"),
			))
		})
	})

	When("the count file disagrees with KRATIX_PROMISE_VERSION", func() {
		It("records the env var version", func() {
			writeFile(lib.HealthDefinitionCountFile, "promiseVersion: v9.9.9\nhealthDefinitions: 1\n")

			Expect(run()).To(Succeed())
			Expect(currentStatus()["healthStatus"]).To(HaveKeyWithValue("expectedPromiseVersion", "v2.0.0"))
		})
	})

	When("status.yaml sets healthStatus", func() {
		BeforeEach(func() {
			writeFile("status.yaml", "healthStatus:\n  state: healthy\n  expectedPromiseVersion: v2.0.0\n")
		})

		It("rejects the update and leaves the object untouched", func() {
			Expect(run()).To(MatchError(ContainSubstring("'healthStatus' is a kratix managed status field")))
			Expect(currentStatus()["healthStatus"]).To(Equal(existingHealthStatus()))
		})
	})

	DescribeTable("leaves healthStatus untouched",
		func(setup func()) {
			setup()
			Expect(run()).To(Succeed())
			Expect(currentStatus()["healthStatus"]).To(Equal(existingHealthStatus()))
		},
		Entry("when there is no count file", func() {}),
		Entry("when the Promise version is empty", func() {
			writeCountFile("1")
			params.PromiseVersion = ""
		}),
		Entry("when the Promise is unversioned", func() {
			writeCountFile("1")
			params.PromiseVersion = v1alpha1.PlaceholderPromiseVersion
		}),
		Entry("in a promise workflow", func() {
			writeCountFile("1")
			params.WorkflowType = v1alpha1.WorkflowTypePromise
		}),
		Entry("in a delete workflow", func() {
			writeCountFile("1")
			GinkgoT().Setenv(v1alpha1.KratixActionEnvVar, string(v1alpha1.WorkflowActionDelete))
		}),
		Entry("in a delete workflow with a malformed count file", func() {
			writeFile(lib.HealthDefinitionCountFile, "[")
			GinkgoT().Setenv(v1alpha1.KratixActionEnvVar, string(v1alpha1.WorkflowActionDelete))
		}),
		Entry("in a dry run", func() {
			writeCountFile("1")
			GinkgoT().Setenv(v1alpha1.KratixDryRunEnvVar, "true")
		}),
	)
})
