package system_test

import (
	"os"
	"strings"
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Destinations", func() {
	Describe("status and events", func() {
		var destinationName string
		BeforeEach(func() {
			destinationName = "worker-3"

			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

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
				platform.Kubectl("patch", "secret", "minio-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"minioadmin"}}`)
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

			By("firing the success event to the default GitStateStore", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "gitstatestores", "default"), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("GitStateStore \"default\" is ready"))
			})

			// The reconciliation logic is common to both types of state stores, so we
			// can just test the BucketStateStore from now on.
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
				}).Should(ContainSubstring("Could not fetch Secret"))
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
			platform.Kubectl("apply", "-f", "assets/destination/destination-test-store.yaml")

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
				platform.Kubectl("patch", "secret", "minio-credentials", "--type=merge", "-p", `{"stringData":{"accessKeyID":"invalid"}}`)

				By("showing `Ready` as False in the State Store", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "bucketstatestores", "destination-test-store")
					}, "30s").Should(ContainSubstring("False"))
				})

				By("firing a failure event to the State Store", func() {
					Eventually(func() string {
						describeOutput := strings.Split(platform.Kubectl("describe", "bucketstatestores", "destination-test-store"), "\n")
						return describeOutput[len(describeOutput)-2]
					}).Should(ContainSubstring("Error writing test file"))
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
})
