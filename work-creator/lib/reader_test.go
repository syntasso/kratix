package lib_test

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Reader", func() {
	var (
		tempDir    string
		fakeClient dynamic.Interface

		subject   lib.Reader
		params    *helpers.Parameters
		ctx       context.Context
		outputLog *os.File
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "reader-test")
		Expect(err).NotTo(HaveOccurred())

		ctx = context.Background()

		params = &helpers.Parameters{
			InputDir:        tempDir,
			ObjectGroup:     "group",
			ObjectName:      "name-foo",
			ObjectVersion:   "version",
			ObjectNamespace: "ns-foo",
			CRDPlural:       "thekinds",

			PromiseName:   "test-promise",
			ClusterScoped: false,
		}

		originalGetInputDir := helpers.GetParametersFromEnv
		originalGetK8sClient := helpers.GetK8sClient
		helpers.GetParametersFromEnv = func() *helpers.Parameters {
			return params
		}
		helpers.GetK8sClient = func() (dynamic.Interface, error) {
			return fakeClient, nil
		}
		DeferCleanup(func() {
			helpers.GetParametersFromEnv = originalGetInputDir
			helpers.GetK8sClient = originalGetK8sClient
		})

		promise := v1alpha1.Promise{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "platform.kratix.io/v1alpha1",
				Kind:       "Promise",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-promise",
			},
		}

		scheme := runtime.NewScheme()
		v1alpha1.AddToScheme(scheme)

		fakeClient = fake.NewSimpleDynamicClient(scheme,
			newUnstructured("group/version", "TheKind", "ns-foo", "name-foo"),
			&promise,
		)

		outputLog, err = os.CreateTemp(tempDir, "output")
		Expect(err).NotTo(HaveOccurred())

		subject = lib.Reader{
			Out: outputLog,
		}
	})

	AfterEach(func() {
		outputLog.Close()
		Expect(os.RemoveAll(tempDir)).To(Succeed())
	})

	It("should write the object to a file", func() {
		Expect(subject.Run(ctx)).To(Succeed())

		_, err := os.Stat(params.GetObjectPath())
		Expect(err).NotTo(HaveOccurred())

		fileContent, err := os.ReadFile(params.GetObjectPath())
		Expect(err).NotTo(HaveOccurred())

		var object map[string]interface{}
		err = yaml.Unmarshal(fileContent, &object)
		Expect(err).NotTo(HaveOccurred())

		Expect(object["apiVersion"]).To(Equal("group/version"))
		Expect(object["kind"]).To(Equal("TheKind"))
		Expect(object["metadata"].(map[string]interface{})["name"]).To(Equal("name-foo"))
		Expect(object["metadata"].(map[string]interface{})["namespace"]).To(Equal("ns-foo"))
		Expect(object["spec"].(map[string]interface{})["test"]).To(Equal("bar"))

		outputContents, err := os.ReadFile(outputLog.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(outputContents)).To(SatisfyAll(
			ContainSubstring("Object file written to"),
			ContainSubstring("apiVersion: group/version"),
		))
	})
})

func newUnstructured(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]interface{}{
				"test": "bar",
			},
		},
	}
}
