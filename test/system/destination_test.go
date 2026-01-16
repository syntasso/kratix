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

	Describe("status and events", func() {
		var destinationName string
		BeforeEach(func() {
			destinationName = "worker-3"
			if os.Getenv("LRE") != "true" {
				platform.Kubectl("apply", "-f", "assets/destination/destination-test-store.yaml")
			}
			platform.Kubectl("apply", "-f", "assets/destination/destination-worker-3.yaml")
		})

		AfterEach(func() {
			if os.Getenv("LRE") == "true" {
				// update the underlying state store with the valid secret
				platform.Kubectl("patch", "bucketstatestore", "destination-test-store", "--type=merge", "-p", `{"spec":{"secretRef":{"name":"aws-s3-credentials"}}}`)
			} else {
				// update the state store secret with the valid credentials when running in KinD
				platform.Kubectl("patch", "secret", "rustfs-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"minioadmin"}}`, "-n", "kratix-platform-system")
			}
			platform.Kubectl("delete", "-f", "assets/destination/destination-worker-3.yaml")
			platform.Kubectl("delete", "secret", "new-state-store-secret", "--ignore-not-found")
		})

		It("properly set the conditions and events", func() {
			// The test uses the destination-test-store BucketStateStore for testing most
			// of the logic, but we still need to check that the GitStateStore becomes
			// ready in case there are git-specific issues with checking readiness.
			By("showing `Ready` as true in the default GitStateStore", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "gitstatestores", "default")
				}).Should(ContainSubstring("True"))
			})

			// The reconciliation logic is common to both types of state stores, so we
			// can just test the BucketStateStore from now on.
			By("showing `Ready` as true in the State Store", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "bucketstatestores", "destination-test-store")
				}).Should(ContainSubstring("True"))
			})

			By("showing `Ready` as true in the Destination", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event to the Destination", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring(`Destination "worker-3" is ready`))
			})

			// set stateStoreRef to a non-existing one
			platform.Kubectl("patch", "destinations", destinationName, "--type=merge", "-p", `{"spec":{"stateStoreRef":{"name":"non-existing"}}}`)

			By("showing `Ready` as False in the Destination when the State Store does not exist", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("False"))
			})

			By("firing a failure event to the Destination", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("Failed to write test documents"))
			})

			// restore the ready condition
			platform.Kubectl("apply", "-f", "assets/destination/destination-worker-3.yaml")

			By("showing `Ready` as true", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event to the Destination", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring(`Destination "worker-3" is ready`))
			})

			// update the underlying state store with a non-existent secret
			platform.Kubectl("patch", "bucketstatestore", "destination-test-store", "--type=merge", "-p", `{"spec":{"secretRef":{"name":"non-existent-secret"}}}`)
			By("showing `Ready` as False in the State Store when the State Store secret does not exist", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "bucketstatestores", "destination-test-store")
				}).Should(ContainSubstring("False"))
			})

			By("firing a failure event to the State Store", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "bucketstatestores", "destination-test-store"), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("Error initialising writer: secret \"non-existent-secret\" not found in namespace \"kratix-platform-system\""))
			})

			By("showing `Ready` as False in the Destination when the State Store secret does not exist", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("False"))
			})

			By("firing a failure event to the Destination", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("Failed to write test documents"))
			})

			// restore the ready condition
			if os.Getenv("LRE") == "true" {
				platform.Kubectl("patch", "bucketstatestore", "destination-test-store", "--type=merge", "-p", `{"spec":{"secretRef":{"name":"aws-s3-credentials"}}}`)
			} else {
				platform.Kubectl("apply", "-f", "assets/destination/destination-test-store.yaml")
			}

			By("showing `Ready` as true in the State Store", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "bucketstatestores", "destination-test-store")
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event to the State Store", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "bucketstatestores", "destination-test-store"), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("BucketStateStore \"destination-test-store\" is ready"))
			})

			By("showing `Ready` as true in the Destination", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event to the Destination", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring(`Destination "worker-3" is ready`))
			})

			if os.Getenv("LRE") != "true" {
				// update the underlying state store secret with invalid credentials (non-LRE only)
				platform.Kubectl("patch", "secret", "rustfs-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"invalid"}}`, "-n", "kratix-platform-system")

				By("showing `Ready` as False in the State Store", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "bucketstatestores", "destination-test-store")
					}, "30s").Should(ContainSubstring("False"))
				})

				By("firing a failure event to the State Store", func() {
					Eventually(func() string {
						describeOutput := strings.Split(platform.Kubectl("describe", "bucketstatestores", "destination-test-store"), "\n")
						return describeOutput[len(describeOutput)-2]
					}).Should(ContainSubstring("write permission validation failed"))
				})

				By("showing `Ready` as False in the Destination", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "destinations", destinationName)
					}).Should(ContainSubstring("False"))
				})

				By("firing a failure event to the Destination", func() {
					Eventually(func() string {
						describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
						return describeOutput[len(describeOutput)-2]
					}).Should(ContainSubstring("Failed to write test documents"))
				})
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
