package system_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/test/kubeutils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Destinations", Label("destination"), Serial, func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)
	})

	Describe("Git-backed destination status and events", func() {
		var destinationName string
		var stateStoreName string
		var destinationYAML string
		var stateStoreYAML string

		BeforeEach(func() {
			destinationYAML = "assets/destination/destination-worker-git.yaml"
			stateStoreYAML = "assets/destination/destination-git-test-store.yaml"
			destinationName = "test-worker-git"
			stateStoreName = "destination-git-test-store"
			if os.Getenv("LRE") != "true" {
				platform.Kubectl("apply", "-f", stateStoreYAML)
			}
			platform.Kubectl("apply", "-f", destinationYAML)
		})

		AfterEach(func() {
			if os.Getenv("LRE") == "true" {
				// update the underlying state store with the valid secret
				platform.Kubectl("patch", "gitstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"github-credentials"}}}`)
			} else {
				// update the state store secret with the valid credentials when running in KinD
				platform.Kubectl("patch", "secret", "gitea-credentials", "--type=merge", "-p", `{"stringData":{"username":"gitea_admin"}}`)
			}
			platform.Kubectl("delete", "-f", "assets/destination/destination-worker-git.yaml")
		})

		It("properly set the conditions and events", func() {
			WaitReady("gitstatestore", stateStoreName)
			WaitReady("destination", destinationName)
			ExpectEvent("destination", destinationName, `Destination "test-worker-git" is ready`)

			By("failing when the state store ref is not found", func() {
				platform.Kubectl("patch", "destinations", destinationName, "--type=merge", "-p", `{"spec":{"stateStoreRef":{"name":"non-existing"}}}`)

				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Failed to write test documents")
			})

			// restore the ready condition
			platform.Kubectl("apply", "-f", destinationYAML)
			WaitReady("destination", destinationName)

			// update the underlying state store with a non-existent secret
			By("failing when the state store secret does not exist", func() {
				platform.Kubectl("patch", "gitstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"non-existent-secret"}}}`)

				ExpectNotReady("gitstatestore", stateStoreName)
				ExpectEvent(
					"gitstatestore", stateStoreName,
					"Error initialising writer: secret \"non-existent-secret\" not found in namespace \"default\"",
				)
				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Failed to write test documents")
			})

			// restore the ready condition
			if os.Getenv("LRE") == "true" {
				platform.Kubectl("patch", "gitstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"github-credentials"}}}`)
			} else {
				platform.Kubectl("apply", "-f", stateStoreYAML)
			}
			WaitReady("gitstatestore", stateStoreName)
			WaitReady("destination", destinationName)

			if os.Getenv("LRE") != "true" {
				// update the underlying state store secret with invalid credentials (non-LRE only)
				platform.Kubectl("patch", "secret", "gitea-credentials", "--type=merge", "-p", `{"stringData":{"username":"invalid"}}`)

				ExpectNotReady("gitstatestore", stateStoreName)
				ExpectEvent("gitstatestore", stateStoreName, "Authentication failed", "fatal: could not read Username")

				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Authentication failed", "fatal: could not read Username")
			}
		})
	})

	Describe("Bucket-backed destination status and events", func() {
		var destinationName string
		var stateStoreName string
		var destinationYAML string
		var stateStoreYAML string

		BeforeEach(func() {
			destinationYAML = "assets/destination/destination-worker-3.yaml"
			stateStoreYAML = "assets/destination/destination-test-store.yaml"
			destinationName = "worker-3"
			stateStoreName = "destination-test-store"
			if os.Getenv("LRE") != "true" {
				platform.Kubectl("apply", "-f", stateStoreYAML)
			}
			platform.Kubectl("apply", "-f", destinationYAML)
		})

		AfterEach(func() {
			if os.Getenv("LRE") == "true" {
				// update the underlying state store with the valid secret
				platform.Kubectl("patch", "bucketstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"aws-s3-credentials"}}}`)
			} else {
				// update the state store secret with the valid credentials when running in KinD
				platform.Kubectl("patch", "secret", "minio-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"minioadmin"}}`)
			}
			platform.Kubectl("delete", "-f", destinationYAML)
			platform.Kubectl("delete", "secret", "new-state-store-secret", "--ignore-not-found")
		})

		It("properly set the conditions and events", func() {
			WaitReady("bucketstatestore", stateStoreName)
			WaitReady("destination", destinationName)
			ExpectEvent("destination", destinationName, `Destination "worker-3" is ready`)

			By("failing when the state store ref is not found", func() {
				platform.Kubectl("patch", "destinations", destinationName, "--type=merge", "-p", `{"spec":{"stateStoreRef":{"name":"non-existing"}}}`)

				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Failed to write test documents")
			})

			// restore the ready condition
			platform.Kubectl("apply", "-f", destinationYAML)
			WaitReady("destination", destinationName)

			// update the underlying state store with a non-existent secret
			By("failing when the state store secret does not exist", func() {
				platform.Kubectl("patch", "bucketstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"non-existent-secret"}}}`)

				ExpectNotReady("bucketstatestore", stateStoreName)
				ExpectEvent(
					"bucketstatestore", stateStoreName,
					"Error initialising writer: secret \"non-existent-secret\" not found in namespace \"default\"",
				)
				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Failed to write test documents")
			})

			// restore the ready condition
			if os.Getenv("LRE") == "true" {
				platform.Kubectl("patch", "bucketstatestore", stateStoreName, "--type=merge", "-p", `{"spec":{"secretRef":{"name":"aws-s3-credentials"}}}`)
			} else {
				platform.Kubectl("apply", "-f", stateStoreYAML)
			}
			WaitReady("bucketstatestore", stateStoreName)
			WaitReady("destination", destinationName)

			if os.Getenv("LRE") != "true" {
				// update the underlying state store secret with invalid credentials (non-LRE only)
				platform.Kubectl("patch", "secret", "minio-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"invalid"}}`)

				ExpectNotReady("bucketstatestore", stateStoreName)
				ExpectEvent("bucketstatestore", stateStoreName, "write permission validation failed")

				ExpectNotReady("destination", destinationName)
				ExpectEvent("destination", destinationName, "Failed to write test documents")
			}
		})
	})

	Describe("filepath modes", func() {
		// We are changing the LRE environment
		// Skipping this test on the LRE to avoid unnecessary work
		if os.Getenv("LRE") != "true" {
			Describe("aggregatedYAML", func() {
				BeforeEach(func() {
					platform.Kubectl("apply", "-f", "assets/destination/destination-aggregated-yaml.yaml")
					platform.Kubectl("apply", "-f", "assets/destination/promise.yaml")

					Eventually(func() string {
						return platform.Kubectl("get", "crds")
					}).Should(ContainSubstring("aggregates.test.kratix.io"))

					platform.Kubectl("apply", "-f", "assets/destination/resources.yaml")
				})

				AfterEach(func() {
					platform.Kubectl("delete", "promises", "aggregate-promise", "--ignore-not-found")
					platform.Kubectl("delete", "-f", "assets/destination/destination-aggregated-yaml.yaml")
				})

				It("aggregates workplacements into a single file", func() {
					By("bundling promise and resources into the same file", func() {
						Eventually(func() []string {
							return filenames(mc("ls", "-r", "kind/kratix/aggregated-yaml"))
						}).Should(ConsistOf(
							"catalog.yaml",
							"kratix-canary-configmap.yaml",
							"kratix-canary-namespace.yaml",
						))

						Eventually(func() []string {
							contents := mc("cat", "kind/kratix/aggregated-yaml/catalog.yaml")
							uContents := parseYAML(contents)
							return kindAndName(uContents)
						}).Should(
							ConsistOf("Namespace:aggregate-test-ns", "ConfigMap:req-1", "ConfigMap:req-2"),
						)
					})

					By("removing the part associated with a resource when the resource gets deleted", func() {
						platform.EventuallyKubectlDelete("aggregates", "req-1")

						Eventually(func() []string {
							contents := mc("cat", "kind/kratix/aggregated-yaml/catalog.yaml")
							uContents := parseYAML(contents)
							return kindAndName(uContents)
						}).Should(
							ConsistOf("Namespace:aggregate-test-ns", "ConfigMap:req-2"),
						)
					})

					By("removing the entire file when the promise gets deleted", func() {
						platform.Kubectl("delete", "promises", "aggregate-promise")
						Eventually(func() string {
							return platform.Kubectl("get", "crds")
						}).ShouldNot(ContainSubstring("aggregates.test.kratix.io"))

						Eventually(func() []string {
							return filenames(mc("ls", "-r", "kind/kratix/aggregated-yaml"))
						}).Should(ConsistOf(
							"kratix-canary-configmap.yaml",
							"kratix-canary-namespace.yaml",
						))
					})
				})
			})
		}
	})

	Describe("cleanup all", func() {
		if os.Getenv("LRE") != "true" {
			When("destination cleanup strategy is set to 'all'", func() {
				destinationName := "cleanup-all"
				BeforeEach(func() {
					platform.Kubectl("apply", "-f", "assets/destination/destination-cleanup-all.yaml")
					platform.Kubectl("apply", "-f", "assets/destination/promise-cleanup-all.yaml")
					Eventually(func() string {
						return platform.Kubectl("get", "crds")
					}).Should(ContainSubstring("cleanupalls.test.kratix.io"))
					platform.Kubectl("apply", "-f", "assets/destination/resources-cleanup-all.yaml")
				})

				AfterEach(func() {
					platform.EventuallyKubectlDelete("-f", "assets/destination/resources-cleanup-all.yaml")
					platform.EventuallyKubectlDelete("-f", "assets/destination/promise-cleanup-all.yaml")
				})

				It("reconciles successfully and delete all workplacements on deletion", func() {
					By("showing `Ready` as true when created", func() {
						Eventually(func() string {
							return platform.Kubectl("get", "destinations", destinationName)
						}).Should(ContainSubstring("True"))
					})

					By("setting clean up finalizer correctly", func() {
						Eventually(func() string {
							return platform.Kubectl("get", "destinations", destinationName, "-ojsonpath='{.metadata.finalizers}'")
						}).Should(ContainSubstring(v1alpha1.KratixPrefix + "destination-cleanup"))
					})

					By("allowing works to be scheduled to", func() {
						Eventually(func() string {
							return mc("ls", "-r", "kind/kratix/cleanup-all")
						}).Should(SatisfyAll(
							ContainSubstring("ns.yaml"),
							ContainSubstring("/configmap.yaml"),
							ContainSubstring("kratix-canary-namespace.yaml"),
							ContainSubstring("kratix-canary-configmap.yaml"),
						))
					})

					By("cleaning up all resources on deletion", func() {
						platform.EventuallyKubectlDelete("destinations", destinationName)
						Eventually(func() string {
							return mc("ls", "kind/kratix/")
						}).ShouldNot(ContainSubstring(destinationName))
					})
				})
			})
		}
	})
})

func kindAndName(objs []*unstructured.Unstructured) []string {
	names := []string{}
	for _, obj := range objs {
		names = append(names, fmt.Sprintf("%s:%s", obj.GetKind(), obj.GetName()))
	}
	return names
}

func filenames(str string) []string {
	lines := strings.Split(str, "\n")

	filenames := []string{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		l := strings.Split(line, " ")
		filenames = append(filenames, l[len(l)-1])
	}

	return filenames
}

func parseYAML(contents string) []*unstructured.Unstructured {
	docs := strings.Split(contents, "---")
	parsedContent := []*unstructured.Unstructured{}
	for _, doc := range docs {
		if doc == "" {
			continue
		}
		u := &unstructured.Unstructured{}
		err := yaml.Unmarshal([]byte(doc), &u)
		Expect(err).ToNot(HaveOccurred())
		parsedContent = append(parsedContent, u)
	}
	return parsedContent
}

func mc(args ...string) string {
	command := exec.Command("mc", args...)
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	EventuallyWithOffset(1, session).Should(gexec.Exit(0))

	return string(session.Out.Contents())
}

func WaitReady(kind, name string) {
	GinkgoHelper()
	Eventually(func() string {
		return platform.Kubectl("get", kind, name)
	}).Should(ContainSubstring("True"))
}

func ExpectNotReady(kind, name string) {
	GinkgoHelper()
	Eventually(func() string {
		return platform.Kubectl("get", kind, name)
	}, "30s").Should(ContainSubstring("False"))
}

func ExpectEvent(kind, name string, events ...string) {
	GinkgoHelper()
	Eventually(func() bool {
		describeOutput := strings.Split(platform.Kubectl("describe", kind, name), "\n")
		eventOutput := describeOutput[len(describeOutput)-2]
		for _, e := range events {
			if strings.Contains(eventOutput, e) {
				return true
			}
		}
		return false
	}).Should(BeTrue())
}
