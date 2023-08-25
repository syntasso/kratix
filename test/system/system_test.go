package system_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

type destination struct {
	context string
}

var (
	promisePath               = "./assets/bash-promise/promise.yaml"
	promiseWithSchedulingPath = "./assets/bash-promise/promise-with-destination-selectors.yaml"

	workerCtx = "--context=kind-worker"
	platCtx   = "--context=kind-platform"

	timeout  = time.Second * 90
	interval = time.Second * 2

	platform = destination{context: "--context=kind-platform"}
	worker   = destination{context: "--context=kind-worker"}
)

const pipelineTimeout = "--timeout=89s"

// This test uses a unique Bash Promise which allows us to easily test behaviours
// in the pipeline.
//
// # The promise dependencies has a single resource, the `bash-wcr-namespace` Namespace
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
kind: bash
metadata:
  name: %s
spec:
  container0Cmd: |
    %s
  container1Cmd: |
    %s`
	storeType string
)

var _ = Describe("Kratix", func() {
	BeforeSuite(func() {
		initK8sClient()
		storeType = "bucket"
		if os.Getenv("SYSTEM_TEST_STORE_TYPE") == "git" {
			storeType = "git"
		}
		fmt.Println("Running system tests with statestore " + storeType)
	})

	Describe("Promise lifecycle", func() {
		It("successfully manages the promise lifecycle", func() {
			By("installing the promise", func() {
				platform.kubectl("apply", "-f", promisePath)

				platform.eventuallyKubectl("get", "crd", "bash.test.kratix.io")
				worker.eventuallyKubectl("get", "namespace", "bash-wcr-namespace")
			})

			By("deleting a promise", func() {
				platform.kubectl("delete", "promise", "bash")

				Eventually(func(g Gomega) {
					g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring("bash-wcr-namespace"))
					g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("bash"))
					g.Expect(platform.kubectl("get", "crd")).ShouldNot(ContainSubstring("bash"))
				}, timeout, interval).Should(Succeed())
			})
		})

		Describe("Resource requests", func() {
			BeforeEach(func() {
				platform.kubectl("apply", "-f", promisePath)
				platform.eventuallyKubectl("get", "crd", "bash.test.kratix.io")
			})

			It("executes the pipelines and schedules the work", func() {
				rrName := "rr-test"

				c1Command := `kop="delete"
							if [ "${KRATIX_OPERATION}" != "delete" ]; then kop="create"
								echo "message: My awesome status message" > /kratix/metadata/status.yaml
								echo "key: value" >> /kratix/metadata/status.yaml
								mkdir -p /kratix/output/foo/
								echo "{}" > /kratix/output/foo/example.json
							fi
			                kubectl ${kop} namespace imperative-$(yq '.metadata.name' /kratix/input/object.yaml)`

				c2Command := `kubectl create namespace declarative-$(yq '.metadata.name' /kratix/input/object.yaml) --dry-run=client -oyaml > /kratix/output/namespace.yaml`

				commands := []string{c1Command, c2Command}

				platform.kubectl("apply", "-f", requestWithNameAndCommand(rrName, commands...))

				By("executing the pipeline pod", func() {
					platform.kubectl("wait", "--for=condition=PipelineCompleted", "bash", rrName, pipelineTimeout)
				})

				By("deploying the contents of /kratix/output to the worker destination", func() {
					platform.eventuallyKubectl("get", "namespace", "imperative-rr-test")
					worker.eventuallyKubectl("get", "namespace", "declarative-rr-test")
				})

				By("mirroring the directory and files from /kratix/output to the statestore", func() {
					Expect(listFilesInStateStore("worker-1", "default", "bash", rrName)).To(ConsistOf("foo/example.json", "namespace.yaml"))
				})

				By("updating the resource status", func() {
					Expect(platform.kubectl("get", "bash", rrName)).To(ContainSubstring("My awesome status message"))
					Expect(platform.kubectl("get", "bash", rrName, "-o", "jsonpath='{.status.key}'")).To(ContainSubstring("value"))
				})

				By("deleting the resource request", func() {
					platform.kubectl("delete", "bash", rrName)

					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "bash")).NotTo(ContainSubstring(rrName))
						g.Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring("imperative-rr-test"))
						g.Expect(worker.kubectl("get", "namespace")).NotTo(ContainSubstring("declarative-rr-test"))
					}, timeout, interval).Should(Succeed())
				})

				By("deleting the pipeline pods", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.kubectl("get", "pods")).NotTo(ContainSubstring("configure"))
						g.Expect(platform.kubectl("get", "pods")).NotTo(ContainSubstring("delete"))
					}, timeout, interval).Should(Succeed())
				})
			})

			When("an existing resource request is updated", func() {
				const requestName = "update-test"
				var oldNamespaceName string
				BeforeEach(func() {
					oldNamespaceName = fmt.Sprintf("old-%s", requestName)
					createNamespace := fmt.Sprintf(
						`kubectl create namespace %s --dry-run=client -oyaml > /kratix/output/old-namespace.yaml`,
						oldNamespaceName,
					)
					platform.kubectl("apply", "-f", requestWithNameAndCommand(requestName, createNamespace))
					platform.kubectl("wait", "--for=condition=PipelineCompleted", "bash", requestName, pipelineTimeout)
					worker.eventuallyKubectl("get", "namespace", oldNamespaceName)
					Eventually(func() []string {
						return minioListFiles("worker-1", "default", "bash", requestName)
					}, timeout, interval).Should(ConsistOf("old-namespace.yaml"))
				})

				It("executes the update lifecycle", func() {
					newNamespaceName := fmt.Sprintf("new-%s", requestName)
					updateNamespace := fmt.Sprintf(
						`kubectl create namespace %s --dry-run=client -oyaml > /kratix/output/new-namespace.yaml`,
						newNamespaceName,
					)
					platform.kubectl("apply", "-f", requestWithNameAndCommand(requestName, updateNamespace))

					Eventually(func() []string {
						return minioListFiles("worker-1", "default", "bash", requestName)
					}, timeout, interval).Should(ConsistOf("new-namespace.yaml"))
					By("redeploying the contents of /kratix/output to the worker destination", func() {
						Eventually(func() string {
							return worker.kubectl("get", "namespace")
						}, "30s").Should(
							SatisfyAll(
								Not(ContainSubstring(oldNamespaceName)),
								ContainSubstring(newNamespaceName),
							),
						)
					})
				})
			})
			AfterEach(func() {
				platform.kubectl("delete", "promise", "bash")
				Eventually(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("bash"))
			})
		})
	})

	Describe("Scheduling", func() {
		// Worker destination (BucketStateStore):
		// - environment: dev
		// - security: high

		// Platform destination (GitStateStore):
		// - environment: platform

		// PromiseScheduling:
		// - security: high
		BeforeEach(func() {
			platform.kubectl("label", "destination", "worker-1", "security=high")
			platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_gitops-tk-resources.yaml", storeType))
			platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_statestore.yaml", storeType))
			platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_kratix_destination.yaml", storeType))
			platform.kubectl("apply", "-f", promiseWithSchedulingPath)
			platform.eventuallyKubectl("get", "crd", "bash.test.kratix.io")
		})

		AfterEach(func() {
			platform.kubectl("label", "destination", "worker-1", "security-", "pci-")
			platform.kubectl("delete", "-f", promiseWithSchedulingPath)
			platform.kubectl("delete", "-f", fmt.Sprintf("./assets/%s/platform_kratix_destination.yaml", storeType))
			platform.kubectl("delete", "-f", fmt.Sprintf("./assets/%s/platform_statestore.yaml", storeType))
		})

		It("schedules resources to the correct Destinations", func() {
			By("reconciling on new Destinations", func() {
				By("only the worker Destination getting the dependency", func() {
					worker.eventuallyKubectl("get", "namespace", "bash-wcr-namespace")
					Expect(platform.kubectl("get", "namespace")).NotTo(ContainSubstring("bash-wcr-namespace"))
				})

				By("labeling the platform Destination, it gets the dependencies assigned", func() {
					platform.kubectl("label", "destination", "platform-1", "security=high")
					platform.eventuallyKubectl("get", "namespace", "bash-wcr-namespace")
				})
			})

			By("respecting the pipeline's scheduling", func() {
				pipelineCmd := `echo "[{\"matchLabels\":{\"pci\":\"true\"}}]" > /kratix/metadata/destination-selectors.yaml
				kubectl create namespace rr-2-namespace --dry-run=client -oyaml > /kratix/output/ns.yaml`
				platform.kubectl("apply", "-f", requestWithNameAndCommand("rr-2", pipelineCmd))

				platform.kubectl("wait", "--for=condition=PipelineCompleted", "bash", "rr-2", pipelineTimeout)

				By("only scheduling the work when a Destination label matches", func() {
					Consistently(func() string {
						return platform.kubectl("get", "namespace") + "\n" + worker.kubectl("get", "namespace")
					}, "10s").ShouldNot(ContainSubstring("rr-2-namespace"))

					platform.kubectl("label", "destination", "worker-1", "pci=true")

					worker.eventuallyKubectl("get", "namespace", "rr-2-namespace")
				})
			})
		})
	})
})

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

	file, err := ioutil.TempFile("", "kratix-test")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	args := []interface{}{name}
	for _, cmd := range normalisedCmds {
		args = append(args, cmd)
	}

	contents := fmt.Sprintf(baseRequestYAML, args...)
	fmt.Fprintln(GinkgoWriter, "Resource Request:")
	fmt.Fprintln(GinkgoWriter, contents)

	ExpectWithOffset(1, ioutil.WriteFile(file.Name(), []byte(contents), 0644)).NotTo(HaveOccurred())

	return file.Name()
}

// run a command until it exits 0
func (c destination) eventuallyKubectl(args ...string) string {
	args = append(args, c.context)
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		command := exec.Command("kubectl", args...)
		session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session).Should(gexec.Exit(0))
		content = string(session.Out.Contents())
	}, timeout, interval).Should(Succeed(), strings.Join(args, " "))
	return content
}

// run command and return stdout. Errors if exit code non-zero
func (c destination) kubectl(args ...string) string {
	args = append(args, c.context)
	command := exec.Command("kubectl", args...)
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
	return string(session.Out.Contents())
}

func listFilesInStateStore(destinationName, namespace, promiseName, resourceName string) []string {
	paths := []string{}
	resourceSubDir := filepath.Join(destinationName, "resources", namespace, promiseName, resourceName)
	if storeType == "bucket" {
		endpoint := "localhost:31337"
		secretAccessKey := "minioadmin"
		accessKeyID := "minioadmin"
		useSSL := false
		bucketName := "kratix"

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
	} else {
		dir, err := ioutil.TempDir("", "kratix-test-repo")
		Expect(err).NotTo(HaveOccurred())
		defer os.RemoveAll(filepath.Dir(dir))

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
		Expect(err).NotTo(HaveOccurred())

		absoluteDir := filepath.Join(dir, resourceSubDir)
		err = filepath.Walk(absoluteDir, func(path string, info os.FileInfo, err error) error {
			if !info.IsDir() {
				path, err := filepath.Rel(absoluteDir, path)
				Expect(err).NotTo(HaveOccurred())
				paths = append(paths, path)
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())

	}
	return paths
}
