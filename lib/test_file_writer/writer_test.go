package utils_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/syntasso/kratix/api/v1alpha1"
	utils "github.com/syntasso/kratix/lib/test_file_writer"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("TestFileWriter", func() {

	var (
		fakeWriter        *writersfakes.FakeStateStoreWriter
		workloadName      string
		kratixNamespace   *v1.Namespace
		namespaceFileName string
		kratixConfigMap   *v1.ConfigMap
		configMapFileName string
		filePathMode      string
		dependenciesDir   string
		resourcesDir      string
	)

	Describe("WriteTestFiles", func() {
		BeforeEach(func() {
			fakeWriter = &writersfakes.FakeStateStoreWriter{}
			workloadName = "kratix-canary"
			kratixNamespace = &v1.Namespace{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Namespace",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
			}
			namespaceFileName = "kratix-canary-namespace.yaml"
			filePathMode = v1alpha1.FilepathModeNestedByMetadata
			dependenciesDir = "dependencies"
			resourcesDir = "resources"

			kratixConfigMap = &v1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kratix-info",
					Namespace: "kratix-worker-system",
				},
				Data: map[string]string{
					"canary": "the confirms your infrastructure is reading from Kratix state stores",
				},
			}
			configMapFileName = "kratix-canary-configmap.yaml"
		})

		It("writes files to the resource and dependencies paths", func() {
			Expect(utils.WriteTestFiles(fakeWriter, filePathMode, dependenciesDir, resourcesDir, workloadName)).To(Succeed())
			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
			dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
			Expect(dir).To(Equal(""))
			Expect(workPlacementName).To(Equal(workloadName))
			Expect(workloadsToCreate[0]).To(MatchFields(IgnoreExtras, Fields{
				"Filepath": Equal(dependenciesDir + "/" + namespaceFileName),
				"Content":  ContainSubstring(kratixNamespace.GetName()),
			}))
			Expect(workloadsToDelete).To(BeEmpty())

			dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(1)
			Expect(dir).To(Equal(""))
			Expect(workPlacementName).To(Equal(workloadName))
			Expect(workloadsToCreate[0]).To(MatchFields(IgnoreExtras, Fields{
				"Filepath": Equal(resourcesDir + "/" + configMapFileName),
				"Content":  ContainSubstring(kratixConfigMap.GetName()),
			}))
			Expect(workloadsToDelete).To(BeEmpty())
		})

		Context("when filePathMode is 'none'", func() {
			BeforeEach(func() {
				filePathMode = v1alpha1.FilepathModeNone
			})
			It("writes files to the resource and dependencies paths without a destination prefix", func() {
				Expect(utils.WriteTestFiles(fakeWriter, filePathMode, dependenciesDir, resourcesDir, workloadName)).To(Succeed())
				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(dir).To(Equal(""))
				Expect(workPlacementName).To(Equal(workloadName))
				Expect(workloadsToCreate).To(HaveLen(1))

				Expect(workloadsToCreate[0]).To(MatchFields(IgnoreExtras, Fields{
					"Filepath": Equal(namespaceFileName),
					"Content":  ContainSubstring(kratixNamespace.GetName()),
				}))
				Expect(workloadsToDelete).To(BeEmpty())

				dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(1)
				Expect(dir).To(Equal(""))
				Expect(workPlacementName).To(Equal(workloadName))
				Expect(workloadsToCreate).To(HaveLen(1))
				Expect(workloadsToCreate[0]).To(MatchFields(IgnoreExtras, Fields{
					"Filepath": Equal(configMapFileName),
					"Content":  ContainSubstring(kratixConfigMap.GetName()),
				}))
				Expect(workloadsToDelete).To(BeEmpty())
			})
		})
	})
})
