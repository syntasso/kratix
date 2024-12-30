package lib_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline/lib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

type TestSetup struct {
	tmpDir          string
	promisePath     string
	objectPath      string
	objectData      []byte
	uHealthPipeline *unstructured.Unstructured
}

var _ = Describe("HealthDefinitionCreator", func() {
	var (
		setup TestSetup
	)

	BeforeEach(func() {
		setup = setupTest()
	})

	AfterEach(func() {
		os.RemoveAll(setup.tmpDir)
	})

	It("should return a health definition", func() {
		healthDefinition, err := lib.CreateHealthDefinition(setup.objectPath, setup.promisePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthDefinition).ToNot(BeNil())
		Expect(healthDefinition).To(MatchAllFields(Fields{
			"APIVersion": Equal("platform.kratix.io/v1alpha1"),
			"Kind":       Equal("HealthDefinition"),
			"Metadata": MatchFields(IgnoreExtras, Fields{
				"Name":      Equal("default-test-object-test-promise"),
				"Namespace": Equal("default"),
			}),
			"Spec": MatchFields(IgnoreExtras, Fields{
				"PromiseRef": MatchAllFields(Fields{
					"Name": Equal("test-promise"),
				}),
				"ResourceRef": MatchAllFields(Fields{
					"Name":      Equal("test-object"),
					"Namespace": Equal("default"),
				}),
				"Input":    Equal(string(setup.objectData)),
				"Workflow": Equal(setup.uHealthPipeline),
				"Schedule": Equal("@daily"),
			}),
		}))
	})

	It("should return an error if the promise is not found", func() {
		_, err := lib.CreateHealthDefinition(setup.objectPath, "non-existing.yaml")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("error reading promise file"))
	})

	It("should return an error if the object is not found", func() {
		_, err := lib.CreateHealthDefinition("non-existing.yaml", setup.promisePath)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("error reading object file"))
	})
})

func pipelineToUnstructured(pipeline v1alpha1.Pipeline) *unstructured.Unstructured {
	healthObjMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
	Expect(err).NotTo(HaveOccurred())
	uPipeline := &unstructured.Unstructured{Object: healthObjMap}
	uPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
	uPipeline.SetKind("Pipeline")
	return uPipeline
}

func setupTest() TestSetup {
	tmpDir, err := os.MkdirTemp("", "health-definition-creator-test")
	Expect(err).ToNot(HaveOccurred())

	uHealthPipeline := pipelineToUnstructured(v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "health-pipeline"},
		Spec: v1alpha1.PipelineSpec{
			Containers: []v1alpha1.Container{{Name: "a-container-name", Image: "test-image:latest"}},
		},
	})

	promise := &v1alpha1.Promise{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "Promise",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-promise",
		},
		Spec: v1alpha1.PromiseSpec{
			HealthChecks: &v1alpha1.HealthChecks{
				Resource: &v1alpha1.HealthCheckDefinition{
					Schedule: "@daily",
					Workflow: uHealthPipeline,
				},
			},
		},
	}

	objectMap := map[string]any{
		"apiVersion": "test.kratix.io/v1alpha1",
		"kind":       "TestObject",
		"metadata": map[string]any{
			"name":      "test-object",
			"namespace": "default",
		},
		"spec": map[string]any{
			"someField": "someValue",
			"anotherField": map[string]any{
				"nestedField": "nestedValue",
			},
		},
	}
	object := &unstructured.Unstructured{Object: objectMap}

	promisePath := filepath.Join(tmpDir, "promise.yaml")
	promiseData, err := yaml.Marshal(promise)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(promisePath, promiseData, 0644)).To(Succeed())

	objectPath := filepath.Join(tmpDir, "object.yaml")
	objectData, err := yaml.Marshal(object)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(objectPath, objectData, 0644)).To(Succeed())

	return TestSetup{
		tmpDir:          tmpDir,
		promisePath:     promisePath,
		objectPath:      objectPath,
		uHealthPipeline: uHealthPipeline,
		objectData:      objectData,
	}
}
