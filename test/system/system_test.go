package system_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/yaml"
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
	interval            = time.Second * 2

	worker   *destination
	platform *destination

	endpoint        string
	secretAccessKey string
	accessKeyID     string
	useSSL          bool
	bucketName      string
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
	storeType string

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

				bashPromise.Spec.Workflows.Resource.Configure[1].Object["spec"].(map[string]interface{})["containers"].(map[string]interface{})["command"].([]string)[2] = "kubectl create configmap REPLACEBASH-2nd-workflow-new -o yaml > /kratix/output/configmap.yaml"

				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

				worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.withExitCode(1).eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace)
				worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				worker.eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName+"-new")
				platform.eventuallyKubectl("get", "namespace", declarativePlatformNamespace)
			})

			By("deleting a promise", func() {
				platform.eventuallyKubectlDelete("promise", bashPromiseName)

				worker.withExitCode(1).eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
				worker.withExitCode(1).eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)
				platform.withExitCode(1).eventuallyKubectl("get", "namespace", declarativePlatformNamespace)
				platform.withExitCode(1).eventuallyKubectl("get", "promise", bashPromiseName)
				platform.withExitCode(1).eventuallyKubectl("get", "crd", bashPromise.Name)
				worker.eventuallyKubectl("get", "configmap", secondPromiseConfigureWorkflowName)
			})
		})

		When("the promise has requirements that are fulfilled", func() {
			var tmpDir string
			BeforeEach(func() {
				var err error
				tmpDir, err = os.MkdirTemp(os.TempDir(), "systest")
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
					platform.eventuallyKubectl("apply", "-f", catAndReplacePromiseRelease(tmpDir, promiseReleasePath, bashPromiseName))

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
					platform.eventuallyKubectlDelete("-f", catAndReplacePromiseRelease(tmpDir, promiseReleasePath, bashPromiseName))

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
			It("executes the pipelines and schedules the work to the appropriate destinations", func() {
				platform.eventuallyKubectl("apply", "-f", cat(bashPromise))
				platform.eventuallyKubectl("get", "crd", crd.Name)
				worker.eventuallyKubectl("get", "namespace", declarativeWorkerNamespace)

				rrName := bashPromiseName + "rr-test"
				platform.kubectl("apply", "-f", exampleBashRequest(rrName, "old"))

				oldRRDeclarativePlatformNamespace := "declarative-platform-only-" + rrName + "-old"
				oldRRDeclarativeWorkerNamespace := "declarative-" + rrName + "-old"
				oldRRImperativePlatformNamespace := "imperative-" + rrName + "-old"

				pipelineLabel := fmt.Sprintf("kratix-promise-resource-request-id=%s-%s", bashPromiseName, rrName)
				By("executing the pipeline pod", func() {
					platform.eventuallyKubectl("wait", "--for=condition=PipelineCompleted", bashPromiseName, rrName, pipelineTimeout)
					Eventually(func() string {
						return platform.eventuallyKubectl("get", "pods", "--selector", pipelineLabel)
					}, timeout, interval).Should(ContainSubstring("Completed"))
				})

				By("deploying the contents of /kratix/output/platform to the platform destination only", func() {
					platform.eventuallyKubectl("get", "namespace", oldRRDeclarativePlatformNamespace)
					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(oldRRDeclarativePlatformNamespace))
				})

				By("deploying the remaining contents of /kratix/output to the worker destination", func() {
					worker.eventuallyKubectl("get", "namespace", oldRRDeclarativeWorkerNamespace)
				})

				By("the imperative API call in the pipeline to the platform cluster succeeding", func() {
					platform.eventuallyKubectl("get", "namespace", oldRRImperativePlatformNamespace)
				})

				By("mirroring the directory and files from /kratix/output to the statestore", func() {

					if getEnvOrDefault("TEST_SKIP_BUCKET_CHECK", "false") != "true" {
						Expect(listFilesMinIOInStateStore(worker.name, "default", bashPromiseName, rrName)).To(ConsistOf("5058f/foo/example.json", "5058f/namespace.yaml"))
					}
				})

				By("updating the resource status", func() {
					Eventually(func() string {
						return platform.kubectl("get", bashPromiseName, rrName)
					}, timeout, interval).Should(ContainSubstring("My awesome status message"))
					Eventually(func() string {
						return platform.kubectl("get", bashPromiseName, rrName, "-o", "jsonpath='{.status.key}'")
					}, timeout, interval).Should(ContainSubstring("value"))
				})

				newRRDeclarativePlatformNamespace := "declarative-platform-only-" + rrName + "-new"
				newRRDeclarativeWorkerNamespace := "declarative-" + rrName + "-new"
				newRRImperativePlatformNamespace := "imperative-" + rrName + "-new"
				By("updating the resource request", func() {
					platform.kubectl("apply", "-f", exampleBashRequest(rrName, "new"))

					Eventually(func() string {
						return worker.kubectl("get", "namespace")
					}, timeout).Should(
						SatisfyAll(
							Not(ContainSubstring(oldRRDeclarativeWorkerNamespace)),
							ContainSubstring(newRRDeclarativeWorkerNamespace),
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
					}, timeout, interval).Should(Succeed())
				})

				By("deleting the pipeline pods", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "pods", "--selector", pipelineLabel)).NotTo(ContainSubstring("configure"))
						g.Expect(platform.kubectl("get", "pods", "--selector", pipelineLabel)).NotTo(ContainSubstring("delete"))
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
					Expect(worker.eventuallyKubectl("get", "namespace", declarativeStaticWorkerNamespace, "-o=yaml")).To(ContainSubstring("modifydepsinpipeline"))
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

				By("deploying the contents of /kratix/output to the worker destination", func() {
					platform.eventuallyKubectl("get", "namespace", rrImperativeNamespace)
					worker.eventuallyKubectl("get", "namespace", rrDeclarativeNamespace)
				})

				By("updating the promise", func() {
					//Promise has:
					// API:
					//    v1alpha2 as the new stored version, with a 3rd command field
					//    which has the default command of creating an additional
					//    namespace declarative-rr-test-v1alpha2
					// Pipeline:
					//    resource
					//      Extra container to run the 3rd command field
					//    promise
					//      rename namespace from bash-dep-namespace-v1alpha1 to
					//      bash-dep-namespace-v1alpha2
					// Dependencies:
					//    Renamed the namespace to bash-dep-namespace-v1alpha2
					updatedPromise := readPromiseAndReplaceWithUniqueBashName(promiseV1Alpha2Path, bashPromiseName)
					platform.eventuallyKubectl("apply", "-f", cat(updatedPromise))

					// imperativePlatformNamespace = fmt.Sprintf(templateImperativePlatformNamespace, bashPromiseName)
					// declarativePlatformNamespace = fmt.Sprintf(templateDeclarativePlatformNamespace, bashPromiseName)
					updatedDeclarativeWorkerNamespace := fmt.Sprintf(templateDeclarativeWorkerNamespace, bashPromiseName, "v1alpha2")
					updatedDeclarativeStaticWorkerNamespace := fmt.Sprintf(templateDeclarativeStaticWorkerNamespace, bashPromiseName, "v1alpha2")

					worker.eventuallyKubectl("get", "namespace", updatedDeclarativeStaticWorkerNamespace)
					worker.eventuallyKubectl("get", "namespace", updatedDeclarativeWorkerNamespace)
					worker.eventuallyKubectl("get", "namespace", rrDeclarativeNamespace)
					worker.eventuallyKubectl("get", "namespace", "declarative-"+rrName+"-v1alpha2")
					platform.eventuallyKubectl("get", "namespace", rrImperativeNamespace)

					Eventually(func(g Gomega) {
						namespaces := worker.kubectl("get", "namespaces")
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeStaticWorkerNamespace))
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeWorkerNamespace))
						g.Expect(namespaces).NotTo(ContainSubstring(declarativeStaticWorkerNamespace))
					}, timeout, interval).Should(Succeed())
				})

				platform.eventuallyKubectlDelete("promise", bashPromiseName)
				Eventually(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring(bashPromiseName))
			})
		})
	})

	Describe("PromiseRelease", func() {
		When("a PromiseRelease is installed", func() {
			BeforeEach(func() {
				tmpDir, err := os.MkdirTemp(os.TempDir(), "systest")
				Expect(err).NotTo(HaveOccurred())
				promiseBytes, err := kyaml.Marshal(bashPromise)
				Expect(err).NotTo(HaveOccurred())
				bashPromiseEncoded := base64.StdEncoding.EncodeToString(promiseBytes)
				platform.eventuallyKubectl("set", "env", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster", fmt.Sprintf("%s=%s", bashPromiseName, bashPromiseEncoded))
				platform.eventuallyKubectl("rollout", "status", "-n=kratix-platform-system", "deployment", "kratix-promise-release-test-hoster")
				platform.eventuallyKubectl("apply", "-f", catAndReplacePromiseRelease(tmpDir, promiseReleasePath, bashPromiseName))
				os.RemoveAll(tmpDir)
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

					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(depNamespaceName))

					// Promise Configure Workflow DestinationSelectors
					platform.kubectl("label", "destination", worker.name, bashPromiseUniqueLabel, "security-")
					Consistently(func() string {
						return worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring(depNamespaceName))

					platform.kubectl("label", "destination", worker.name, bashPromiseUniqueLabel, "security=high")

					worker.eventuallyKubectl("get", "namespace", depNamespaceName)
					Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring(depNamespaceName))
				})

				By("labeling the platform Destination, it gets the dependencies assigned", func() {
					platform.kubectl("label", "destination", platform.name, "security=high", bashPromiseUniqueLabel)
					platform.eventuallyKubectl("get", "namespace", depNamespaceName)
				})

			})

			// Remove the labels again so we can check the same flow for resource requests
			platform.kubectl("label", "destination", worker.name, removeBashPromiseUniqueLabel)

			By("respecting the pipeline's scheduling", func() {
				pipelineCmd := `echo "[{\"matchLabels\":{\"pci\":\"true\"}}]" > /kratix/metadata/destination-selectors.yaml
				kubectl create namespace rr-2-namespace --dry-run=client -oyaml > /kratix/output/ns.yaml`
				platform.kubectl("apply", "-f", requestWithNameAndCommand("rr-2", pipelineCmd))

				platform.eventuallyKubectl("wait", "--for=condition=PipelineCompleted", bashPromiseName, "rr-2", pipelineTimeout)

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
})

func exampleBashRequest(name, namespaceSuffix string) string {
	request := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.kratix.io/v1alpha1",
			"kind":       bashPromiseName,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"container0Cmd": fmt.Sprintf(`
					set -x
					if [ "${KRATIX_WORKFLOW_ACTION}" = "configure" ]; then :
						echo "message: My awesome status message" > /kratix/metadata/status.yaml
						echo "key: value" >> /kratix/metadata/status.yaml
						mkdir -p /kratix/output/foo/
						echo "{}" > /kratix/output/foo/example.json
						kubectl get namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s || kubectl create namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s
						exit 0
					fi
					kubectl delete namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)-%[1]s
				`, namespaceSuffix, namespaceSuffix),
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
// This means we need a more roboust approach for deleting Promises
func (c destination) eventuallyKubectlDelete(kind, name string) string {
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		command := exec.Command("kubectl", "get", "--context="+c.context, kind, name)
		session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, shortTimeout).Should(gexec.Exit())
		//If it doesn't exist, lets succeed
		if strings.Contains(string(session.Out.Contents()), "not found") {
			return
		}

		command = exec.Command("kubectl", "delete", "--context="+c.context, kind, name)
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
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	if c.ignoreExitCode {
		EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit())
	} else {
		EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
	}
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
func listFilesMinIOInStateStore(destinationName, namespace, promiseName, resourceName string) []string {
	paths := []string{}
	resourceSubDir := filepath.Join(destinationName, "resources", namespace, promiseName, resourceName)

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	Expect(err).ToNot(HaveOccurred())

	//worker-1/resources/default/redis/rr-test
	objectCh := minioClient.ListObjects(context.TODO(), bucketName, minio.ListObjectsOptions{
		Prefix:    resourceSubDir,
		Recursive: true,
	})

	for object := range objectCh {
		Expect(object.Err).NotTo(HaveOccurred())

		path, err := filepath.Rel(resourceSubDir, object.Key)
		Expect(err).ToNot(HaveOccurred())
		paths = append(paths, path)
	}
	return paths
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
