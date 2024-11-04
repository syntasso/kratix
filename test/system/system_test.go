package system_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/onsi/ginkgo/v2/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/syntasso/kratix/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

type destination struct {
	context        string
	ignoreExitCode bool
	exitCode       int
	name           string
}

var (
	promisePath            = "./assets/bash-promise/promise.yaml"
	promiseReleasePath     = "./assets/bash-promise/promise-release.yaml"
	promiseV1Alpha2Path    = "./assets/bash-promise/promise-v1alpha2.yaml"
	promisePermissionsPath = "./assets/bash-promise/roles-for-promise.yaml"
	resourceRequestPath    = "./assets/requirements/example-rr.yaml"
	promiseWithRequirement = "./assets/requirements/promise-with-requirement.yaml"

	timeout             = time.Second * 400
	shortTimeout        = time.Second * 200
	consistentlyTimeout = time.Second * 20
	interval            = time.Millisecond * 10

	worker   *destination
	platform *destination

	endpoint        string
	secretAccessKey string
	accessKeyID     string
	useSSL          bool
	bucketName      string

	authenticatedEndpoint   = true
	unauthenticatedEndpoint = false
)

const pipelineTimeout = "--timeout=89s"

// This test uses a unique Bash Promise which allows us to easily test behaviours
// in the pipeline.
//
// # The promise dependencies has a single resource, the `bash-dep-namespace` Namespace
//
// Below is the template for a RR to this Promise. It provides a hook to run an
// arbitrary Bash command in each of the two Pipeline containers. An example use
// case may be wanting to test status works which requires a written to a
// specific location. To do this you can write a RR that has the following:
//
// container0Cmd: echo "statusTest: pass" > /kratix/metadata/status.yaml
//
// The commands will be run in the pipeline container that is named in the spec.
// The Promise pipeline will always have a set number of containers, though
// a command is not required for every container.
// e.g. `container0Cmd` is run in the first container of the pipeline.
var (
	baseRequestYAML = `apiVersion: test.kratix.io/v1alpha1
kind: %s
metadata:
  name: %s
spec:
  container0Cmd: |
    %s
  container1Cmd: |
    %s`

	templateImperativePlatformNamespace      = "%s-platform-imperative"
	templateDeclarativePlatformNamespace     = "%s-platform-declarative"
	templateDeclarativeWorkerNamespace       = "%s-worker-declarative-%s"
	templateDeclarativeStaticWorkerNamespace = "%s-static-decl-%s"

	imperativePlatformNamespace      string
	declarativePlatformNamespace     string
	declarativeWorkerNamespace       string
	declarativeStaticWorkerNamespace string
	bashPromise                      *v1alpha1.Promise
	bashPromiseName                  string
	bashPromiseUniqueLabel           string
	removeBashPromiseUniqueLabel     string
	crd                              *v1.CustomResourceDefinition
)

var _ = Describe("Kratix", func() {
	BeforeEach(func() {
		bashPromise = generateUniquePromise(promisePath)
		bashPromiseName = bashPromise.Name
		bashPromiseUniqueLabel = bashPromiseName + "=label"
		removeBashPromiseUniqueLabel = bashPromiseName + "-"
		imperativePlatformNamespace = fmt.Sprintf(templateImperativePlatformNamespace, bashPromiseName)
		declarativePlatformNamespace = fmt.Sprintf(templateDeclarativePlatformNamespace, bashPromiseName)
		declarativeWorkerNamespace = fmt.Sprintf(templateDeclarativeWorkerNamespace, bashPromiseName, "v1alpha1")
		declarativeStaticWorkerNamespace = fmt.Sprintf(templateDeclarativeStaticWorkerNamespace, bashPromiseName, "v1alpha1")
		var err error
		crd, err = bashPromise.GetAPIAsCRD()
		Expect(err).NotTo(HaveOccurred())

		platform.kubectl("label", "destination", worker.name, bashPromiseUniqueLabel)
	})

	AfterEach(func() {
		if CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.kubectl("label", "destination", worker.name, removeBashPromiseUniqueLabel)
			platform.kubectl("label", "destination", platform.name, removeBashPromiseUniqueLabel)
			platform.eventuallyKubectlDelete("promisereleases", bashPromiseName)
			platform.eventuallyKubectlDelete("promise", bashPromiseName)
			platform.eventuallyKubectlDelete("namespace", imperativePlatformNamespace)
			platform.eventuallyKubectlDelete("namespace", "imperative-"+bashPromiseName+"-test")
			platform.eventuallyKubectlDelete("clusterrole", bashPromiseName+"-default-resource-pipeline-credentials")
			platform.eventuallyKubectlDelete("clusterrolebinding", bashPromiseName+"-default-resource-pipeline-credentials")
			platform.eventuallyKubectlDelete("serviceaccount", bashPromiseName+"-existing-custom-sa")
		}
	})

	Describe("Promise lifecycle", func() {
		var secondPromiseConfigureWorkflowName string
		BeforeEach(func() {
			secondPromiseConfigureWorkflowName = fmt.Sprintf("%s-2nd-workflow", bashPromiseName)
		})

		It("can install, update, and delete a promise", func() {
			By("installing the promise", func() {
				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

				platform.eventuallyKubectl("get", "crd", crd.Name)
				platform.eventuallyKubectl("get", "namespace", imperativePlatformNamespace)
				platform.eventuallyKubectl("get", "namespace", declarativePlatformNamespace)
				worker.eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				worker.eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName)
			})

			updatedDeclarativeStaticWorkerNamespace := declarativeStaticWorkerNamespace + "-new"
			By("updating the promise", func() {
				bashPromise.Spec.Dependencies[0] = v1alpha1.Dependency{
					Unstructured: unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "v1",
							"kind":       "Namespace",
							"metadata": map[string]interface{}{
								"name": updatedDeclarativeStaticWorkerNamespace,
							},
						},
					},
				}

				newCmd := fmt.Sprintf(
					"kubectl create configmap %s-new -o yaml --namespace default --dry-run=client > /kratix/output/configmap.yaml",
					secondPromiseConfigureWorkflowName,
				)
				bashPromise.Spec.Workflows.Promise.Configure[1].Object["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})["args"].([]interface{})[0] = newCmd

				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

				worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.withExitCode(1).eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				worker.eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName+"-new")
				worker.withExitCode(1).eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName)
				platform.eventuallyKubectl("get", "namespace", declarativePlatformNamespace)
			})

			By("deleting a promise", func() {
				platform.eventuallyKubectlDelete("promise", bashPromiseName)

				worker.withExitCode(1).eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.withExitCode(1).eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				worker.withExitCode(1).eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName+"-new")
				platform.withExitCode(1).eventuallyKubectl("get", "namespace", declarativePlatformNamespace)
				platform.withExitCode(1).eventuallyKubectl("get", "namespace", imperativePlatformNamespace)
				platform.withExitCode(1).eventuallyKubectl("get", "promise", bashPromiseName)
				platform.withExitCode(1).eventuallyKubectl("get", "crd", bashPromise.Name)
			})
		})

		When("the promise has requirements that are fulfilled", func() {
			var tmpDir string
			BeforeEach(func() {
				var err error
				tmpDir, err = os.MkdirTemp(os.TempDir(), "systest-"+bashPromiseName)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				os.RemoveAll(tmpDir)
			})

			It("can fulfil resource requests once requirements are met", func() {
				promise := readPromiseAndReplaceWithUniqueBashName(promiseWithRequirement, bashPromiseName)
				By("the Promise being Unavailable when installed without requirements", func() {
					platform.eventuallyKubectl("apply", "-f", cat(promise))
					Eventually(func(g Gomega) {
						platform.eventuallyKubectl("get", "promise", "redis")
						g.Expect(platform.kubectl("get", "promise", "redis")).To(ContainSubstring("Unavailable"))
						platform.eventuallyKubectl("get", "crd", "redis.marketplace.kratix.io")
					}, timeout, interval).Should(Succeed())
				})

				By("allowing resource requests to be created in Pending state", func() {
					platform.eventuallyKubectl("apply", "-f", resourceRequestPath)

					Eventually(func(g Gomega) {
						platform.eventuallyKubectl("get", "redis")
						g.Expect(platform.kubectl("get", "redis", "example-rr")).To(ContainSubstring("Pending"))
					}, timeout, interval).Should(Succeed())
				})

				By("the Promise being Available once requirements are installed", func() {
					promiseBytes, err := kyaml.Marshal(bashPromise)
					Expect(err).NotTo(HaveOccurred())
					bashPromiseEncoded := base64.StdEncoding.EncodeToString(promiseBytes)
					platform.eventuallyKubectl("set", "env", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster", fmt.Sprintf("%s=%s", bashPromiseName, bashPromiseEncoded))
					platform.eventuallyKubectl("rollout", "status", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster")
					platform.eventuallyKubectl("apply", "-f", promiseReleaseForHttp(tmpDir, promiseReleasePath, bashPromiseName, unauthenticatedEndpoint))

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "promise")).Should(ContainSubstring(bashPromiseName))
						g.Expect(platform.kubectl("get", "crd")).Should(ContainSubstring(bashPromiseName))
						g.Expect(platform.kubectl("get", "promiserelease")).Should(ContainSubstring(bashPromiseName))
					}, timeout, interval).Should(Succeed())

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "promise", "redis")).To(ContainSubstring("Available"))
					}, timeout, interval).Should(Succeed())
				})

				By("creating the 'pending' resource requests", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "redis", "example-rr")).To(ContainSubstring("Resource requested"))
					}, timeout, interval).Should(Succeed())
				})

				By("marking the Promise as Unavailable when the requirements are deleted", func() {
					platform.eventuallyKubectlDelete("-f", promiseReleaseForHttp(tmpDir, promiseReleasePath, bashPromiseName, unauthenticatedEndpoint))

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring(bashPromiseName))
					}, timeout, interval).Should(Succeed())

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "promise", "redis")).To(ContainSubstring("Unavailable"))
					}, timeout, interval).Should(Succeed())
				})

				By("deleting the promise", func() {
					platform.eventuallyKubectlDelete("-f", promiseWithRequirement)
				})
			})
		})

		Describe("Resource requests", func() {
			BeforeEach(func() {
				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
				platform.eventuallyKubectl("get", "crd", crd.Name)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)

				platform.eventuallyKubectlDelete("namespace", "pipeline-perms-ns")
				platform.eventuallyKubectl("create", "namespace", "pipeline-perms-ns")
			})

			It("executes the pipelines and schedules the work to the appropriate destinations", func() {
				rrName := bashPromiseName + "rr-test"
				platform.kubectl("apply", "-f", exampleBashRequest(rrName, "default", "old"))

				oldRRDeclarativePlatformNamespace := "declarative-platform-only-" + rrName + "-old"
				oldRRDeclarativeWorkerNamespace := "declarative-" + rrName + "-old"
				oldRRImperativePlatformNamespace := "imperative-" + rrName + "-old"
				oldRRDeclarativeConfigMap := fmt.Sprintf("%s-old", rrName)

				firstPipelineName := "first-configure"
				secondPipelineName := "second-configure"
				firstPipelineLabels := fmt.Sprintf(
					"kratix.io/promise-name=%s,kratix.io/resource-name=%s,kratix.io/pipeline-name=%s",
					bashPromiseName,
					rrName,
					firstPipelineName,
				)
				secondPipelineLabels := fmt.Sprintf(
					"kratix.io/promise-name=%s,kratix.io/resource-name=%s,kratix.io/pipeline-name=%s",
					bashPromiseName,
					rrName,
					secondPipelineName,
				)

				By("executing the first pipeline pod", func() {
					Eventually(func() string {
						return platform.eventuallyKubectl("get", "pods", "--selector", firstPipelineLabels)
					}, timeout, interval).Should(ContainSubstring("Completed"))
				})

				By("using the security context defined in the promise", func() {
					podYaml := platform.eventuallyKubectl("get", "pods", "--selector", firstPipelineLabels, "-o=yaml")
					Expect(podYaml).To(ContainSubstring("setInPromise"))
					Expect(podYaml).NotTo(ContainSubstring("setInKratixConfig"))
				})

				By("executing the second pipeline pod", func() {
					Eventually(func() string {
						return platform.eventuallyKubectl("get", "pods", "--selector", secondPipelineLabels)
					}, timeout, interval).Should(ContainSubstring("Completed"))
				})

				By("using the security context defined in the kratix config", func() {
					podYaml := platform.eventuallyKubectl("get", "pods", "--selector", secondPipelineLabels, "-o=yaml")
					Expect(podYaml).To(ContainSubstring("setInKratixConfig"))
					Expect(podYaml).NotTo(ContainSubstring("setInPromise"))
				})

				By("setting the ConfigureWorkflowCompleted condition on the Resource Request", func() {
					platform.eventuallyKubectl("wait", "--for=condition=ConfigureWorkflowCompleted", bashPromiseName, rrName, pipelineTimeout)
				})

				By("deploying the generated resources to the destinations specified in the pipelines", func() {
					By("scheduling the first pipeline outputs to the right places", func() {
						platform.eventuallyKubectl("get", "namespace", oldRRDeclarativePlatformNamespace)
						Consistently(func() string {
							return worker.kubectl("get", "namespace")
						}, "10s").ShouldNot(ContainSubstring(oldRRDeclarativePlatformNamespace))

						worker.eventuallyKubectl("get", "namespace", oldRRDeclarativeWorkerNamespace)
						platform.eventuallyKubectl("get", "namespace", oldRRImperativePlatformNamespace)
					})

					By("scheduling the second pipeline outputs to the right places", func() {
						worker.eventuallyKubectl("get", "configmap", oldRRDeclarativeConfigMap)
					})
				})

				By("mirroring the directory and files from /kratix/output to the statestore", func() {
					if getEnvOrDefault("TEST_SKIP_BUCKET_CHECK", "false") != "true" {
						Expect(listFilesInMinIOStateStore(filepath.Join(worker.name, "resources", "default", bashPromiseName, rrName, firstPipelineName))).To(ConsistOf("5058f/foo/example.json", "5058f/namespace.yaml"))
						Expect(listFilesInMinIOStateStore(filepath.Join(worker.name, "resources", "default", bashPromiseName, rrName, secondPipelineName))).To(ConsistOf("5058f/configmap.yaml"))
					}
				})

				By("updating the resource status", func() {
					Eventually(func() string {
						return platform.kubectl("get", bashPromiseName, rrName)
					}, timeout, interval).Should(ContainSubstring("My awesome status message"))
					Eventually(func() string {
						return platform.kubectl("get", bashPromiseName, rrName, "-o", "jsonpath='{.status.key}'")
					}, timeout, interval).Should(ContainSubstring("value"))
					generation := platform.kubectl("get", bashPromiseName, rrName, "-o", "jsonpath='{.metadata.generation}'")
					Eventually(func() string {
						return platform.kubectl("get", bashPromiseName, rrName, "-o", "jsonpath='{.status.observedGeneration}'")
					}, timeout, interval).Should(Equal(generation))
				})

				newRRDeclarativePlatformNamespace := "declarative-platform-only-" + rrName + "-new"
				newRRDeclarativeWorkerNamespace := "declarative-" + rrName + "-new"
				newRRImperativePlatformNamespace := "imperative-" + rrName + "-new"
				newRRDeclarativeConfigMap := rrName + "-new"
				By("updating the resource request", func() {
					platform.kubectl("apply", "-f", exampleBashRequest(rrName, "default", "new"))

					Eventually(func() string {
						return worker.kubectl("get", "namespace")
					}, timeout).Should(
						SatisfyAll(
							Not(ContainSubstring(oldRRDeclarativeWorkerNamespace)),
							ContainSubstring(newRRDeclarativeWorkerNamespace),
						),
					)
					Eventually(func() string {
						return worker.kubectl("get", "configmap")
					}, timeout).Should(
						SatisfyAll(
							Not(ContainSubstring(oldRRDeclarativeConfigMap)),
							ContainSubstring(newRRDeclarativeConfigMap),
						),
					)

					Eventually(func() string {
						return platform.kubectl("get", "namespace")
					}, timeout).Should(
						SatisfyAll(
							Not(ContainSubstring(oldRRDeclarativePlatformNamespace)),
							ContainSubstring(newRRDeclarativePlatformNamespace),
							ContainSubstring(newRRImperativePlatformNamespace),
							//Nothing cleans up the old imperative namespace
							ContainSubstring(oldRRImperativePlatformNamespace),
						),
					)
				})

				By("deleting the resource request", func() {
					platform.eventuallyKubectlDelete(bashPromiseName, rrName)

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", bashPromiseName)).NotTo(ContainSubstring(rrName))
						g.Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring(newRRImperativePlatformNamespace))
						g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring(newRRDeclarativeWorkerNamespace))
						g.Expect(worker.kubectl("get", "configmap")).NotTo(ContainSubstring(newRRDeclarativeConfigMap))
					}, timeout, interval).Should(Succeed())
				})

				By("deleting the pipeline pods", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "pods", "--selector", firstPipelineLabels)).NotTo(ContainSubstring("configure"))
						g.Expect(platform.kubectl("get", "pods", "--selector", secondPipelineLabels)).NotTo(ContainSubstring("configure"))
						g.Expect(platform.kubectl("get", "pods", "--selector", firstPipelineLabels)).NotTo(ContainSubstring("delete"))
						g.Expect(platform.kubectl("get", "pods", "--selector", secondPipelineLabels)).NotTo(ContainSubstring("delete"))
					}, timeout, interval).Should(Succeed())
				})

				platform.eventuallyKubectlDelete("promise", bashPromiseName)
				platform.kubectl("delete", "namespace", oldRRImperativePlatformNamespace)
				Eventually(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring(bashPromiseName))
			})
		})

		When("A Promise is updated", func() {
			It("propagates the changes and re-runs all the pipelines", func() {
				By("installing and requesting v1alpha1 promise", func() {
					platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

					platform.eventuallyKubectl("get", "crd", crd.Name)
				})

				rrName := bashPromiseName + "rr-test"

				c1Command := `kop="delete"
							echo "$KRATIX_WORKFLOW_ACTION"
							if [ "${KRATIX_WORKFLOW_ACTION}" != "delete" ]; then kop="create"
								echo "message: My awesome status message" > /kratix/metadata/status.yaml
								echo "key: value" >> /kratix/metadata/status.yaml
								mkdir -p /kratix/output/foo/
								echo "{}" > /kratix/output/foo/example.json
			          kubectl get namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml) || kubectl create namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)
								exit 0
							fi
			                kubectl delete namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)`

				c2Command := `kubectl create namespace declarative-$(yq '.metadata.name' /kratix/input/object.yaml) --dry-run=client -oyaml > /kratix/output/namespace.yaml`

				commands := []string{c1Command, c2Command}

				platform.kubectl("apply", "-f", requestWithNameAndCommand(rrName, commands...))

				rrImperativeNamespace := "imperative-" + rrName
				rrDeclarativeNamespace := "declarative-" + rrName
				rrDeclarativeConfigMap := rrName + "-default"

				By("deploying the contents of /kratix/output to the worker destination", func() {
					platform.eventuallyKubectl("get", "namespace", rrImperativeNamespace)
					worker.eventuallyKubectl("get", "namespace", rrDeclarativeNamespace)
					worker.eventuallyKubectl("get", "configmap", rrDeclarativeConfigMap)
				})

				roleName := strings.Trim(platform.kubectl("get", "role", "-l",
					userPermissionRoleLabels(bashPromiseName, "resource", "configure", "first-configure"),
					"-o=jsonpath='{.items[0].metadata.name}'"), "'")
				roleCreationTimestamp := platform.kubectl("get", "role", roleName, "-o=jsonpath='{.metadata.creationTimestamp}'")

				bindingName := strings.Trim(platform.kubectl("get", "rolebinding", "-l",
					userPermissionRoleLabels(bashPromiseName, "resource", "configure", "first-configure"),
					"-o=jsonpath='{.items[0].metadata.name}'"), "'")
				bindingCreationTimestamp := platform.kubectl("get", "rolebinding", bindingName, "-o=jsonpath='{.metadata.creationTimestamp}'")

				specificNamespaceClusterRoleName := strings.Trim(platform.kubectl("get", "ClusterRole", "-l",
					userPermissionClusterRoleLabels(bashPromiseName, "resource", "configure", "first-configure", "pipeline-perms-ns"),
					"-o=jsonpath='{.items[0].metadata.name}'"), "'")
				specificNamespaceClusterRoleCreationTimestamp := platform.kubectl("get", "ClusterRole", specificNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")

				allNamespaceClusterRoleName := strings.Trim(platform.kubectl("get", "ClusterRole", "-l",
					userPermissionClusterRoleLabels(bashPromiseName, "resource", "configure", "first-configure", "kratix_all_namespaces"),
					"-o=jsonpath='{.items[0].metadata.name}'"), "'")
				allNamespaceClusterRoleCreationTimestamp := platform.kubectl("get", "ClusterRole", allNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")

				By("updating the promise", func() {
					//Promise has:
					// API:
					//    v1alpha2 as the new stored version, with a 3rd command field
					//    which has the default command of creating an additional
					//    namespace declarative-rr-test-v1alpha2
					// Pipeline:
					//    resource
					//      Extra container to run the 3rd command field
					//      rename configmap from bashrr-test-default to bashrr-test-default-v2
					//    promise
					//      rename namespace from bash-dep-namespace-v1alpha1 to
					//      bash-dep-namespace-v1alpha2
					// Dependencies:
					//    Renamed the namespace to bash-dep-namespace-v1alpha2
					updatedPromise := readPromiseAndReplaceWithUniqueBashName(promiseV1Alpha2Path, bashPromiseName)
					platform.eventuallyKubectl("apply", "-f", cat(updatedPromise))

					updatedDeclarativeWorkerNamespace := fmt.Sprintf(templateDeclarativeWorkerNamespace, bashPromiseName, "v1alpha2")
					updatedDeclarativeStaticWorkerNamespace := fmt.Sprintf(templateDeclarativeStaticWorkerNamespace, bashPromiseName, "v1alpha2")

					worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
					worker.eventuallyKubectl("get", "namespace", updatedDeclarativeWorkerNamespace)
					worker.eventuallyKubectl("get", "namespace", rrDeclarativeNamespace)
					worker.eventuallyKubectl("get", "namespace", "declarative-"+rrName+"-v1alpha2")
					worker.eventuallyKubectl("get", "configmap", rrDeclarativeConfigMap+"-v2")
					platform.eventuallyKubectl("get", "namespace", rrImperativeNamespace)

					Eventually(func(g Gomega) {
						namespaces := worker.kubectl("get", "namespaces")
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeStaticWorkerNamespace))
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeWorkerNamespace))
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeStaticWorkerNamespace))
					}, timeout, interval).Should(Succeed())

					worker.withExitCode(1).eventuallyKubectl("get", "configmap", rrDeclarativeConfigMap)
				})

				By("not recreating permission objects", func() {
					Expect(platform.kubectl("get", "role", roleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(roleCreationTimestamp))
					Expect(platform.kubectl("get", "rolebinding", bindingName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(bindingCreationTimestamp))
					Expect(platform.kubectl("get", "clusterrole", specificNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(specificNamespaceClusterRoleCreationTimestamp))
					Expect(platform.kubectl("get", "clusterrole", allNamespaceClusterRoleName, "-o=jsonpath='{.metadata.creationTimestamp}'")).To(Equal(allNamespaceClusterRoleCreationTimestamp))
				})

				platform.eventuallyKubectlDelete("promise", bashPromiseName)
				Eventually(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring(bashPromiseName))
			})
		})

		When("creating multiple resource requests in different namespaces", func() {
			rrOneNamespace := "default"
			rrTwoNamespace := "test"

			BeforeEach(func() {
				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
				platform.eventuallyKubectl("get", "crd", crd.Name)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				platform.kubectl("create", "ns", rrTwoNamespace)

				platform.eventuallyKubectlDelete("namespace", "pipeline-perms-ns")
				platform.eventuallyKubectl("create", "namespace", "pipeline-perms-ns")
			})

			It("creates separate bindings for request namespaces and schedules works to correct destinations", func() {
				rrOneName := bashPromiseName + "rr-test1"
				rrTwoName := bashPromiseName + "rr-test2"
				platform.kubectl("apply", "-f", exampleBashRequest(rrOneName, rrOneNamespace, "mul"))
				platform.kubectl("apply", "-f", exampleBashRequest(rrTwoName, rrTwoNamespace, "mul"))

				By("creating separate role bindings for resource requests from two namespaces", func() {
					Eventually(func(g Gomega) {
						bindingNames := platform.kubectl("get", "rolebindings", "-n", v1alpha1.SystemNamespace, "-l",
							userPermissionRoleLabels(bashPromiseName, "resource", "configure", "second-configure"))
						g.Expect(bindingNames).To(SatisfyAll(
							ContainSubstring(fmt.Sprintf("%s-resource-configure-second-configure-%s", bashPromiseName, rrOneNamespace)),
							ContainSubstring(fmt.Sprintf("%s-resource-configure-second-configure-%s", bashPromiseName, rrTwoNamespace))))
					}, timeout, interval).Should(Succeed())
				})

				By("executing pipelines successfully", func() {
					platform.eventuallyKubectl("wait", "-n", rrOneNamespace, "--for=condition=ConfigureWorkflowCompleted", bashPromiseName, rrOneName, pipelineTimeout)
					platform.eventuallyKubectl("wait", "-n", rrTwoNamespace, "--for=condition=ConfigureWorkflowCompleted", bashPromiseName, rrTwoName, pipelineTimeout)
				})

				rrOneDeclarativePlatformNamespace := "declarative-platform-only-" + rrOneName + "-mul"
				rrOneDeclarativeWorkerNamespace := "declarative-" + rrOneName + "-mul"
				rrOneImperativePlatformNamespace := "imperative-" + rrOneName + "-mul"
				rrOneDeclarativeConfigMap := fmt.Sprintf("%s-mul", rrOneName)

				rrTwoDeclarativePlatformNamespace := "declarative-platform-only-" + rrTwoName + "-mul"
				rrTwoDeclarativeWorkerNamespace := "declarative-" + rrTwoName + "-mul"
				rrTwoImperativePlatformNamespace := "imperative-" + rrTwoName + "-mul"
				rrTwoDeclarativeConfigMap := fmt.Sprintf("%s-mul", rrTwoName)

				By("creating generated resources", func() {
					platform.eventuallyKubectl("get", "namespace", rrOneDeclarativePlatformNamespace)
					worker.eventuallyKubectl("get", "namespace", rrOneDeclarativeWorkerNamespace)
					platform.eventuallyKubectl("get", "namespace", rrOneImperativePlatformNamespace)
					worker.eventuallyKubectl("get", "configmap", rrOneDeclarativeConfigMap)

					platform.eventuallyKubectl("get", "namespace", rrTwoDeclarativePlatformNamespace)
					worker.eventuallyKubectl("get", "namespace", rrTwoDeclarativeWorkerNamespace)
					platform.eventuallyKubectl("get", "namespace", rrTwoImperativePlatformNamespace)
					worker.eventuallyKubectl("get", "configmap", rrTwoDeclarativeConfigMap)
				})

				By("cleaning up declarative resources when deleting resource requests", func() {
					platform.eventuallyKubectlDelete(bashPromiseName, rrOneName, "-n", rrOneNamespace)
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", bashPromiseName, "-A")).NotTo(ContainSubstring(rrOneName))
						g.Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring(rrOneDeclarativePlatformNamespace))
						g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring(rrOneDeclarativeWorkerNamespace))
						g.Expect(worker.kubectl("get", "configmap")).NotTo(ContainSubstring(rrOneDeclarativeConfigMap))
					}, timeout, interval).Should(Succeed())

					platform.eventuallyKubectlDelete(bashPromiseName, rrTwoName, "-n", rrTwoNamespace)
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", bashPromiseName, "-A")).NotTo(ContainSubstring(rrTwoName))
						g.Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring(rrTwoDeclarativePlatformNamespace))
						g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring(rrTwoDeclarativeWorkerNamespace))
						g.Expect(worker.kubectl("get", "configmap")).NotTo(ContainSubstring(rrTwoDeclarativeConfigMap))
					}, timeout, interval).Should(Succeed())
				})

				platform.eventuallyKubectlDelete("promise", bashPromiseName)
				platform.eventuallyKubectlDelete("ns", rrTwoNamespace)
			})
		})
	})

	Describe("PromiseRelease", func() {
		var tmpDir string
		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp(os.TempDir(), "systest-"+bashPromiseName)
			Expect(err).NotTo(HaveOccurred())
			promiseBytes, err := kyaml.Marshal(bashPromise)
			Expect(err).NotTo(HaveOccurred())
			bashPromiseEncoded := base64.StdEncoding.EncodeToString(promiseBytes)
			platform.
				eventuallyKubectl("set", "env", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster", fmt.Sprintf("%s=%s", bashPromiseName, bashPromiseEncoded))
			platform.eventuallyKubectl("rollout", "status", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster")
		})

		AfterEach(func() {
			os.RemoveAll(tmpDir)
			platform.eventuallyKubectlDelete("deployments", "-n", "kratix-platform-system", "kratix-promise-release-test-hoster")
		})

		When("a PromiseRelease is installed", func() {
			BeforeEach(func() {
				var err error
				tmpDir, err = os.MkdirTemp(os.TempDir(), "systest-"+bashPromiseName)
				Expect(err).NotTo(HaveOccurred())

				platform.eventuallyKubectl("apply", "-f", promiseReleaseForHttp(tmpDir, promiseReleasePath, bashPromiseName, unauthenticatedEndpoint))
			})

			It("can be created and deleted", func() {
				platform.eventuallyKubectl("get", "promiserelease", bashPromiseName)
				platform.eventuallyKubectl("get", "promise", bashPromiseName)
				platform.eventuallyKubectl("get", "crd", crd.Name)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)

				platform.eventuallyKubectlDelete("promiserelease", bashPromiseName)
				Eventually(func(g Gomega) {
					g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring(bashPromiseName))
					g.Expect(platform.kubectl("get", "crd")).ShouldNot(ContainSubstring(crd.Name))
					g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring(bashPromiseName))
					g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring(declarativeWorkerNamespace))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("a PromiseRelease source requires authorization", func() {
			BeforeEach(func() {
				var err error
				tmpDir, err = os.MkdirTemp(os.TempDir(), "systest-"+bashPromiseName)
				Expect(err).NotTo(HaveOccurred())

				platform.eventuallyKubectl("apply", "-f", promiseReleaseForHttp(tmpDir, promiseReleasePath, bashPromiseName, authenticatedEndpoint))
			})

			It("can fetch and apply the Promise", func() {
				platform.eventuallyKubectl("get", "promiserelease", bashPromiseName)
				platform.eventuallyKubectl("get", "promise", bashPromiseName)
				platform.kubectlWait(120, "promise", bashPromiseName, "--for=condition=ConfigureWorkflowCompleted")
			})
		})
	})

	Describe("Scheduling", Serial, func() {
		//The tests start with destinations:
		// Worker destination (BucketStateStore):
		// - environment: dev
		// Platform destination (GitStateStore):
		// - environment: platform

		//The test produces destinations with:
		// Worker destination (BucketStateStore):
		// - environment: dev
		// - security: high

		// Platform destination (GitStateStore):
		// - environment: platform

		// Destination selectors in the promise:
		// - security: high
		It("schedules resources to the correct Destinations", func() {
			By("reconciling on new Destinations", func() {
				depNamespaceName := declarativeStaticWorkerNamespace
				platform.kubectl("label", "destination", worker.name, removeBashPromiseUniqueLabel)

				By("scheduling to the Worker when it gets all the required labels", func() {
					bashPromise.Spec.DestinationSelectors[0] = v1alpha1.PromiseScheduling{
						MatchLabels: map[string]string{
							"security": "high",
						},
					}
					platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
					platform.eventuallyKubectl("get", "crd", crd.Name)

					/*
						The required labels are:
						- security: high (from the promise)
						- uniquepromisename: label (from the promise workflow)
					*/

					// Promise Level DestinationSelectors

					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(depNamespaceName))

					platform.kubectl("label", "destination", worker.name, "security=high")

					worker.eventuallyKubectl("get", "namespace", depNamespaceName)

					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(declarativeWorkerNamespace))

					// Promise Configure Workflow DestinationSelectors
					platform.kubectl("label", "destination", worker.name, bashPromiseUniqueLabel, "security-")

					worker.eventuallyKubectl("get", "namespace", depNamespaceName)

					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(declarativeWorkerNamespace))

					platform.kubectl("label", "destination", worker.name, bashPromiseUniqueLabel, "security=high")

					worker.eventuallyKubectl("get", "namespace", depNamespaceName)
					worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
					Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring(depNamespaceName))
				})

				By("labeling the platform Destination, it gets the dependencies assigned", func() {
					platform.kubectl("label", "destination", platform.name, "security=high", bashPromiseUniqueLabel)
					platform.eventuallyKubectl("get", "namespace", depNamespaceName)
					platform.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				})

			})

			// Remove the labels again so we can check the same flow for resource requests
			platform.kubectl("label", "destination", worker.name, removeBashPromiseUniqueLabel)

			By("respecting the pipeline's scheduling", func() {
				pipelineCmd := `echo "[{\"matchLabels\":{\"pci\":\"true\"}}]" > /kratix/metadata/destination-selectors.yaml
				kubectl create namespace rr-2-namespace --dry-run=client -oyaml > /kratix/output/ns.yaml`
				platform.kubectl("apply", "-f", requestWithNameAndCommand("rr-2", pipelineCmd))

				platform.eventuallyKubectl("wait", "--for=condition=ConfigureWorkflowCompleted", bashPromiseName, "rr-2", pipelineTimeout)

				By("only scheduling the work when a Destination label matches", func() {
					/*
						The required labels are:
						- security: high (from the promise)
						- pci: true (from the resource workflow)
					*/
					Consistently(func() string {
						return platform.kubectl("get", "namespace") + "\n" + worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring("rr-2-namespace"))

					// Add the label defined in the resource.configure workflow
					platform.kubectl("label", "destination", worker.name, "pci=true")

					worker.eventuallyKubectl("get", "namespace", "rr-2-namespace")
				})
			})

			platform.eventuallyKubectlDelete("promise", bashPromiseName)
			platform.kubectl("label", "destination", worker.name, "security-", "pci-", removeBashPromiseUniqueLabel)
			platform.kubectl("label", "destination", platform.name, "security-", removeBashPromiseUniqueLabel)
		})

		// Worker destination (BucketStateStore):
		// - environment: dev

		// Platform destination (GitStateStore):
		// - environment: platform

		// Destination selectors in the promise:
		// - security: high
		// - uniquepromisename: label
		It("allows updates to scheduling", func() {
			bashPromise.Spec.DestinationSelectors[0] = v1alpha1.PromiseScheduling{
				MatchLabels: map[string]string{
					"security": "high",
				},
			}
			platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
			platform.eventuallyKubectl("get", "crd", crd.Name)

			platform.kubectl("label", "destination", platform.name, bashPromiseUniqueLabel, "security-")

			By("only the worker Destination getting the dependency initially", func() {
				Consistently(func() {
					worker.eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				}, consistentlyTimeout, interval)

				Eventually(func() string {
					return platform.kubectl("get", "namespace")
				}, timeout, interval).ShouldNot(ContainSubstring(declarativeStaticWorkerNamespace))

				Consistently(func() string {
					return platform.kubectl("get", "namespace")
				}, consistentlyTimeout, interval).ShouldNot(ContainSubstring(declarativeStaticWorkerNamespace))
			})

			//changes from security: high to environment: platform
			bashPromise.Spec.DestinationSelectors[0] = v1alpha1.PromiseScheduling{
				MatchLabels: map[string]string{
					"environment": "platform",
				},
			}
			platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

			By("scheduling to the new destination and preserving the old orphaned destinations", func() {
				Consistently(func() {
					worker.eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				}, consistentlyTimeout, interval)
				Consistently(func() {
					platform.eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				}, consistentlyTimeout, interval)
			})

			platform.eventuallyKubectlDelete("promise", bashPromiseName)
			platform.kubectl("label", "destination", worker.name, "security-", "pci-", removeBashPromiseUniqueLabel)
			platform.kubectl("label", "destination", platform.name, "security-", removeBashPromiseUniqueLabel)
		})
	})

	Describe("filepathMode set to none", func() {
		It("manages output files from multiple resource requests", func() {
			if getEnvOrDefault("TEST_SKIP_BUCKET_CHECK", "false") != "true" {
				bashPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{{
					MatchLabels: map[string]string{
						"environment": "filepathmode-none",
					},
				}}

				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
				platform.eventuallyKubectl("get", "crd", crd.Name)
				rrNameOne := bashPromiseName + "terraform-1"
				platform.kubectl("apply", "-f", terraformRequest(rrNameOne))
				rrNameTwo := bashPromiseName + "terraform-2"
				platform.kubectl("apply", "-f", terraformRequest(rrNameTwo))

				By("writing output files to the root of stateStore")
				promiseDestName := "filepathmode-none-git"
				Eventually(func() []string {
					return listFilesInGitStateStore(promiseDestName)
				}, shortTimeout, interval).Should(ContainElements("configmap.yaml"))

				resourceDestName := "filepathmode-none-bucket"
				Eventually(func() []string {
					return listFilesInMinIOStateStore(resourceDestName)
				}, shortTimeout, interval).Should(ContainElements(
					"configmap.yaml",
					ContainSubstring(fmt.Sprintf("%s.yaml", rrNameOne)),
					ContainSubstring(fmt.Sprintf("%s.yaml", rrNameTwo)),
				))

				By("removing only files associated with the resource request at deletion")
				platform.kubectl("delete", crd.Name, rrNameOne)
				Eventually(func() []string {
					return listFilesInMinIOStateStore(resourceDestName)
				}, shortTimeout, interval).ShouldNot(ContainElements(
					fmt.Sprintf("%s.yaml", rrNameOne)))
				Expect(listFilesInMinIOStateStore(resourceDestName)).To(ContainElements(
					ContainSubstring(fmt.Sprintf("%s.yaml", rrNameTwo)),
				))

				By("cleaning up files from state store at deletion")
				platform.eventuallyKubectlDelete("promise", bashPromiseName)
				Eventually(func() []string {
					return listFilesInGitStateStore(promiseDestName)
				}, shortTimeout, interval).ShouldNot(ContainElements(
					"configmap.yaml",
					ContainSubstring(".kratix"),
				))

				Eventually(func() []string {
					return listFilesInMinIOStateStore(resourceDestName)
				}, shortTimeout, interval).ShouldNot(ContainElements(
					"configmap.yaml",
					ContainSubstring(".kratix"),
					ContainSubstring(fmt.Sprintf("%s.yaml", rrNameOne)),
					ContainSubstring(fmt.Sprintf("%s.yaml", rrNameTwo)),
				))
			}
		})
	})
})

func terraformRequest(name string) string {
	request := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.kratix.io/v1alpha1",
			"kind":       bashPromiseName,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"container0Cmd": fmt.Sprintf(`
					touch /kratix/output/%s.yaml
					echo "[{\"matchLabels\":{\"type\":\"bucket\"}}]" > /kratix/metadata/destination-selectors.yaml
			`, name),
				"container1Cmd": "exit 0",
			},
		},
	}
	return asFile(request)
}

func exampleBashRequest(name, requestNamespace, namespaceSuffix string) string {
	request := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.kratix.io/v1alpha1",
			"kind":       bashPromiseName,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": requestNamespace,
			},
			"spec": map[string]interface{}{
				"suffix": namespaceSuffix,
				"container0Cmd": fmt.Sprintf(`
					set -x
					if [ "${KRATIX_WORKFLOW_ACTION}" = "configure" ]; then :
						echo "message: My awesome status message" > /kratix/metadata/status.yaml
						echo "key: value" >> /kratix/metadata/status.yaml
						mkdir -p /kratix/output/foo/
						echo "{}" > /kratix/output/foo/example.json
						kubectl get secret,role,service
						kubectl get configmaps -n kratix-platform-system
						kubectl get deployments -n pipeline-perms-ns
						kubectl get namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s || kubectl create namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s
						exit 0
					fi
					kubectl delete namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s
				`, namespaceSuffix),
				"container1Cmd": fmt.Sprintf(`
					kubectl create namespace declarative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s --dry-run=client -oyaml > /kratix/output/namespace.yaml
					mkdir /kratix/output/platform/
					kubectl create namespace declarative-platform-only-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s --dry-run=client -oyaml > /kratix/output/platform/namespace.yaml
					echo "[{\"matchLabels\":{\"environment\":\"platform\"}, \"directory\":\"platform\"}]" > /kratix/metadata/destination-selectors.yaml
			`, namespaceSuffix),
			},
		},
	}
	return asFile(request)
}

func asFile(object unstructured.Unstructured) string {
	file, err := os.CreateTemp("", "kratix-test")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	contents, err := object.MarshalJSON()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	fmt.Fprintln(GinkgoWriter, "Resource Request:")
	fmt.Fprintln(GinkgoWriter, string(contents))

	ExpectWithOffset(1, os.WriteFile(file.Name(), contents, 0644)).NotTo(HaveOccurred())

	return file.Name()
}

func cat(obj interface{}) string {
	file, err := os.CreateTemp(testTempDir, "kratix-test")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	var buf bytes.Buffer
	err = unstructured.UnstructuredJSONScheme.Encode(obj.(runtime.Object), &buf)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	fmt.Fprintln(GinkgoWriter, "Object")
	fmt.Fprintln(GinkgoWriter, buf.String())

	ExpectWithOffset(1, os.WriteFile(file.Name(), buf.Bytes(), 0644)).NotTo(HaveOccurred())

	return file.Name()
}

func requestWithNameAndCommand(name string, containerCmds ...string) string {
	normalisedCmds := make([]string, 2)
	for i := range normalisedCmds {
		cmd := ""
		if len(containerCmds) > i {
			cmd += " " + containerCmds[i]
		}
		normalisedCmds[i] = strings.ReplaceAll(cmd, "\n", ";")
	}

	lci := len(normalisedCmds) - 1
	lastCommand := normalisedCmds[lci]
	if strings.HasSuffix(normalisedCmds[lci], ";") {
		lastCommand = lastCommand[:len(lastCommand)-1]
	}
	normalisedCmds[lci] = lastCommand

	file, err := os.CreateTemp("", "kratix-test")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	args := []interface{}{bashPromiseName, name}
	for _, cmd := range normalisedCmds {
		args = append(args, cmd)
	}

	contents := fmt.Sprintf(baseRequestYAML, args...)
	fmt.Fprintln(GinkgoWriter, "Resource Request:")
	fmt.Fprintln(GinkgoWriter, contents)

	ExpectWithOffset(1, os.WriteFile(file.Name(), []byte(contents), 0644)).NotTo(HaveOccurred())

	return file.Name()
}

// When deleting a Promise a number of things can happen:
// - It takes a long time for finalizers to be removed
// - Kratix restarts, which means the webhook is down temporarily, which results
// in the kubectl delete failing straight away
//   - By time kratix starts back up, it has already been deleted
//
// This means we need a more robust approach for deleting Promises
func (c destination) eventuallyKubectlDelete(args ...string) string {
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		commandArgs := []string{"get", "--context=" + c.context}
		commandArgs = append(commandArgs, args...)
		command := exec.Command("kubectl", commandArgs...)
		session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, time.Second*20).Should(gexec.Exit())
		//If it doesn't exist, lets succeed
		if strings.Contains(string(session.Err.Contents()), "not found") {
			return
		}

		commandArgs = []string{"delete", "--context=" + c.context}
		commandArgs = append(commandArgs, args...)
		command = exec.Command("kubectl", commandArgs...)
		session, err = gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, shortTimeout).Should(gexec.Exit(c.exitCode))
		content = string(session.Out.Contents())
	}, timeout, time.Millisecond).Should(Succeed())
	return content
}

// run a command until it exits 0
func (c destination) eventuallyKubectl(args ...string) string {
	args = append(args, "--context="+c.context)
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		command := exec.Command("kubectl", args...)
		session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, shortTimeout).Should(gexec.Exit(c.exitCode))
		content = string(session.Out.Contents())
	}, timeout, interval).Should(Succeed(), strings.Join(args, " "))
	return content
}

// run command and return stdout. Errors if exit code non-zero
func (c destination) kubectl(args ...string) string {
	args = append(args, "--context="+c.context)
	command := exec.Command("kubectl", args...)
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	fmt.Fprintf(GinkgoWriter, "Running: kubectl %s\n", strings.Join(args, " "))
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	if c.ignoreExitCode {
		EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit())
	} else {
		EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
	}
	return string(session.Out.Contents())
}

// run command and return stdout. Ignores the exit code
func (c destination) kubectlForce(args ...string) string {
	c.ignoreExitCode = true
	return c.kubectl(args...)
}

func (c destination) kubectlWait(waitTimeout int, args ...string) string {
	args = append(args, "--context="+c.context)
	commandArgs := append([]string{"wait", "--timeout", fmt.Sprintf("%ds", waitTimeout)}, args...)
	command := exec.Command("kubectl", commandArgs...)
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	fmt.Fprintf(GinkgoWriter, "Running: kubectl %s\n", strings.Join(commandArgs, " "))
	EventuallyWithOffset(1, session, time.Duration(waitTimeout)*time.Second, interval).Should(gexec.Exit(0))

	return string(session.Out.Contents())
}

func (c destination) clone() destination {
	return destination{
		context:        c.context,
		exitCode:       c.exitCode,
		ignoreExitCode: c.ignoreExitCode,
	}
}

func (c destination) withExitCode(code int) destination {
	newDestination := c.clone()
	newDestination.exitCode = code
	return newDestination
}

func listFilesInGitStateStore(subDir string) []string {
	dir, err := os.MkdirTemp(testTempDir, "git-state-store")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	_, err = git.PlainClone(dir, false, &git.CloneOptions{
		Auth: &http.BasicAuth{
			Username: "gitea_admin",
			Password: "r8sA8CPHD9!bt6d",
		},
		URL:             "https://localhost:31333/gitea_admin/kratix",
		ReferenceName:   plumbing.NewBranchReferenceName("main"),
		SingleBranch:    true,
		Depth:           1,
		NoCheckout:      false,
		InsecureSkipTLS: true,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	var paths []string
	absoluteDir := filepath.Join(dir, subDir)
	err = filepath.Walk(absoluteDir, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			path, err := filepath.Rel(absoluteDir, path)
			Expect(err).NotTo(HaveOccurred())
			paths = append(paths, path)
		}
		return nil
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return paths
}

func listFilesInMinIOStateStore(path string) []string {
	files := []string{}

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	objectCh := minioClient.ListObjects(context.TODO(), bucketName, minio.ListObjectsOptions{
		Prefix:    path,
		Recursive: true,
	})

	for object := range objectCh {
		ExpectWithOffset(1, object.Err).NotTo(HaveOccurred())
		path, err := filepath.Rel(path, object.Key)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		files = append(files, path)
	}
	return files
}

func generateUniquePromise(promisePath string) *v1alpha1.Promise {
	promise := &v1alpha1.Promise{}
	promiseContentBytes, err := os.ReadFile(promisePath)
	Expect(err).NotTo(HaveOccurred())
	randString := string(uuid.NewUUID())[0:5]

	promiseContents := strings.ReplaceAll(string(promiseContentBytes), "REPLACEBASH", "bash"+randString)
	err = yaml.NewYAMLOrJSONDecoder(strings.NewReader(promiseContents), 100).Decode(promise)
	Expect(err).NotTo(HaveOccurred())

	roleAndRolebindingBytes, err := os.ReadFile(promisePermissionsPath)
	Expect(err).NotTo(HaveOccurred())
	roleAndRolebinding := strings.ReplaceAll(string(roleAndRolebindingBytes), "REPLACEBASH", "bash"+randString)

	fileName := "/tmp/bash" + randString
	Expect(os.WriteFile(fileName, []byte(roleAndRolebinding), 0644)).To(Succeed())
	platform.kubectl("apply", "-f", "/tmp/bash"+randString)
	Expect(os.Remove(fileName)).To(Succeed())
	return promise
}

func readPromiseAndReplaceWithUniqueBashName(promisePath, bashPromiseName string) *v1alpha1.Promise {
	promise := &v1alpha1.Promise{}
	promiseContentBytes, err := os.ReadFile(promisePath)
	Expect(err).NotTo(HaveOccurred())

	promiseContents := strings.ReplaceAll(string(promiseContentBytes), "REPLACEBASH", bashPromiseName)
	err = yaml.NewYAMLOrJSONDecoder(strings.NewReader(promiseContents), 100).Decode(promise)
	Expect(err).NotTo(HaveOccurred())
	return promise
}

func userPermissionRoleLabels(promiseName, workflowType, action, pipelineName string) string {
	return fmt.Sprintf("%s=%s,%s=%s,%s=%s,%s=%s",
		v1alpha1.PromiseNameLabel, promiseName,
		v1alpha1.WorkTypeLabel, workflowType,
		v1alpha1.WorkActionLabel, action,
		v1alpha1.PipelineNameLabel, pipelineName)
}

func userPermissionClusterRoleLabels(promiseName, workflowType, action, pipelineName, namespace string) string {
	return fmt.Sprintf("%s,%s=%s",
		userPermissionRoleLabels(promiseName, workflowType, action, pipelineName),
		v1alpha1.UserPermissionResourceNamespaceLabel, namespace)
}

func promiseReleaseForHttp(tmpDir, file, bashPromiseName string, authenticated bool) string {
	bytes, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())

	output := strings.ReplaceAll(string(bytes), "REPLACEBASH", bashPromiseName)
	if authenticated {
		output = strings.ReplaceAll(output, "REPLACEURL", "http://kratix-promise-release-test-hoster.kratix-platform-system:8080/secure/promise/"+bashPromiseName)

		secretNamespacedName := client.ObjectKey{Name: "kratix-promise-release-authorization-header", Namespace: "kratix-platform-system"}
		platform.kubectlForce("create", "secret", "generic", secretNamespacedName.Name, "-n", secretNamespacedName.Namespace, "--from-literal=authorizationHeader=Bearer your-secret-token")
		output = addSecretRefToPromiseRelease(output, secretNamespacedName)
	} else {
		output = strings.ReplaceAll(output, "REPLACEURL", "http://kratix-promise-release-test-hoster.kratix-platform-system:8080/promise/"+bashPromiseName)
	}
	tmpFile := filepath.Join(tmpDir, filepath.Base(file))
	err = os.WriteFile(tmpFile, []byte(output), 0777)
	Expect(err).NotTo(HaveOccurred())

	return tmpFile
}

func addSecretRefToPromiseRelease(promiseRelease string, secretNamespacedName client.ObjectKey) string {
	var err error
	tmpPromiseRelease := &v1alpha1.PromiseRelease{}
	kyaml.Unmarshal([]byte(promiseRelease), &tmpPromiseRelease)
	Expect(err).NotTo(HaveOccurred())

	tmpPromiseRelease.Spec.SourceRef.SecretRef = &corev1.SecretReference{
		Name:      secretNamespacedName.Name,
		Namespace: secretNamespacedName.Namespace,
	}
	outputBytes, err := kyaml.Marshal(tmpPromiseRelease)
	Expect(err).NotTo(HaveOccurred())
	return string(outputBytes)
}
