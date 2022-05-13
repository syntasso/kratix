package integration_test

import (
	"context"
	"io"
	"io/ioutil"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
 Run these tests using `make int-test` to ensure that the correct resources are applied
 to the k8s cluster under test.

 Assumptions:
 1. `kind create cluster --name=platform`
 2. `export IMG=syntasso/kratix-platform:dev`
 3. `make kind-load-image`
 4. `make deploy` has been run. Note: `make int-test` will
 ensure that `deploy` is executed
 5. `make int-test`

 Cleanup:
 k delete databases.postgresql.dev4devs.com database && k delete crd databases.postgresql.dev4devs.com && k delete promises.platform.kratix.io postgres-promise && k delete works.platform.kratix.io work-sample
*/
var (
	k8sClient client.Client
	err       error

	interval = "3s"
	timeout  = "60s"

	redis_gvk = schema.GroupVersionKind{
		Group:   "redis.redis.opstreelabs.in",
		Version: "v1beta1",
		Kind:    "Redis",
	}

	postgres_gvk = schema.GroupVersionKind{
		Group:   "postgresql.dev4devs.com",
		Version: "v1alpha1",
		Kind:    "Database",
	}

	work_gvk = schema.GroupVersionKind{
		Group:   "platform.kratix.io",
		Version: "v1alpha1",
		Kind:    "Work",
	}

	cluster_gvk = schema.GroupVersionKind{
		Group:   "platform.kratix.io",
		Version: "v1alpha1",
		Kind:    "Cluster",
	}
)

const (
	//Targets only cluster-worker-1
	REDIS_CRD                     = "../../config/samples/redis/redis-promise.yaml"
	REDIS_RESOURCE_REQUEST        = "../../config/samples/redis/redis-resource-request.yaml"
	REDIS_RESOURCE_UPDATE_REQUEST = "../../config/samples/redis/redis-resource-update-request.yaml"
	POSTGRES_CRD                  = "../../config/samples/postgres/postgres-promise.yaml"
	//Targets All clusters
	POSTGRES_RESOURCE_REQUEST = "../../config/samples/postgres/postgres-resource-request.yaml"

	//Clusters
	WORKER_CLUSTER_1 = "../../config/samples/platform_v1alpha1_worker_cluster.yaml"
	WORKER_CLUSTER_2 = "../../config/samples/platform_v1alpha1_worker_cluster_2.yaml"
)

var _ = Describe("kratix Platform Integration Test", func() {
	BeforeSuite(func() {
		initK8sClient()

		By("kratix is running")
		Eventually(func() bool {
			pod := getKratixControllerPod()
			return isPodRunning(pod)
		}, timeout, interval).Should(BeTrue())

		By("A Worker Cluster is registered")
		registerWorkerCluster("worker-cluster-1", WORKER_CLUSTER_1)

		By("A Second Worker Cluster is registered")
		registerWorkerCluster("worker-cluster-2", WORKER_CLUSTER_2)
	})

	Describe("Redis Promise lifecycle", func() {
		Describe("Applying Redis Promise", func() {
			It("Applying a Promise CRD manifests a Redis api-resource", func() {
				applyPromiseCRD(REDIS_CRD)

				Eventually(func() bool {
					return isAPIResourcePresent(redis_gvk)
				}, timeout, interval).Should(BeTrue())
			})

			PIt("writes the resources to Minio that are defined in the Promise manifest", func() {
				workloadNamespacedName := types.NamespacedName{
					Name:      "redis-promise-default",
					Namespace: "default",
				}
				Eventually(func() bool {
					resourceName := "redis.redis.redis.opstreelabs.in"
					resourceKind := "CustomResourceDefinition"

					foundCrdWorker1, _ := workerHasCRD(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_1)
					foundResourceWorker1, _ := workerHasResource(workloadNamespacedName, "a-non-crd-resource", "Namespace", WORKER_CLUSTER_1)
					foundCrdWorker2, _ := workerHasCRD(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_2)
					foundResourceWorker2, _ := workerHasResource(workloadNamespacedName, "a-non-crd-resource", "Namespace", WORKER_CLUSTER_1)

					return foundCrdWorker1 && foundCrdWorker2 && foundResourceWorker1 && foundResourceWorker2
				}, timeout, interval).Should(BeTrue(), "has the Redis CRD")
			})
		})

		Describe("Applying Redis resource triggers the TransformationPipeline™", func() {
			It("Applying Redis resource triggers the TransformationPipeline™", func() {
				applyResourceRequest(REDIS_RESOURCE_REQUEST)

				expectedName := types.NamespacedName{
					Name:      "redis-promise-default-default-opstree-redis",
					Namespace: "default",
				}
				Eventually(func() bool {
					return hasResourceBeenApplied(work_gvk, expectedName)
				}, timeout, interval).Should(BeTrue())
			})

			It("should label the created Work resource with a target workload cluster", func() {
				Eventually(func() bool {
					expectedName := types.NamespacedName{
						Name:      "redis-promise-default-default-opstree-redis",
						Namespace: "default",
					}

					work := &platformv1alpha1.Work{}
					err := k8sClient.Get(context.Background(), expectedName, work)
					Expect(err).ToNot(HaveOccurred())

					return metav1.HasLabel(work.ObjectMeta, "cluster")
				}, timeout, interval).Should(BeTrue())
			})

			It("should write a Redis resource to Worker Cluster", func() {
				Eventually(func() bool {
					workloadNamespacedName := types.NamespacedName{
						Name:      "redis-promise-default-default-opstree-redis",
						Namespace: "default",
					}

					//Assert that the Redis resource is present
					resourceName := "opstree-redis"
					resourceKind := "Redis"

					foundCluster1, _ := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_1)
					foundCluster2, _ := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_2)

					if foundCluster1 && foundCluster2 {
						return false
					}

					if !foundCluster1 && !foundCluster2 {
						return false
					}
					return true

				}, timeout, interval).Should(BeTrue())
			})

			It("should update a Redis resource in Minio", func() {
				updateResourceRequest(REDIS_RESOURCE_UPDATE_REQUEST)

				Eventually(func() bool {
					workloadNamespacedName := types.NamespacedName{
						Name:      "redis-promise-default-default-opstree-redis",
						Namespace: "default",
					}

					//Read from Minio
					//Assert that the Redis resource is present
					resourceName := "opstree-redis"
					resourceKind := "Redis"

					foundCluster1, obj1 := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_1)
					foundCluster2, obj2 := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_2)

					if foundCluster1 && foundCluster2 {
						return false
					}

					if !foundCluster1 && !foundCluster2 {
						return false
					}

					//make it work, make it pretty (it works needs to be made pretty)
					var obj unstructured.Unstructured
					if foundCluster1 {
						obj = obj1
					} else if foundCluster2 {
						obj = obj2
					} else {
						return false
					}
					//

					spec := obj.Object["spec"]
					global := spec.(map[string]interface{})["global"]
					password := global.(map[string]interface{})["password"]
					return password == "Opstree@12345"

				}, timeout, interval).Should(BeTrue())
			})
		})
	})

	Describe("Postgres Promise lifecycle", func() {
		Describe("Applying Postgres Promise", func() {
			It("Applying a Promise CRD manifests a Postgres api-resource", func() {
				applyPromiseCRD(POSTGRES_CRD)

				Eventually(func() bool {
					return isAPIResourcePresent(postgres_gvk)
				}, timeout, interval).Should(BeTrue())
			})

			It("writes the resources to Minio that are defined in the Promise manifest", func() {
				// applyPromiseCRD(POSTGRES_CRD)

				//what is this for cluster level?
				workloadNamespacedName := types.NamespacedName{
					Name:      "postgres-promise-default",
					Namespace: "default",
				}
				Eventually(func() bool {
					//Read from Minio
					//Assert that the Postgres resource is present
					resourceName := "databases.postgresql.dev4devs.com"
					resourceKind := "CustomResourceDefinition"

					found, _ := workerHasCRD(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_1)
					return found
				}, timeout, interval).Should(BeTrue(), "has the Postgres CRD")
			})
		})

		Describe("Applying Postgres resource triggers the TransformationPipeline™", func() {
			It("Applying Postgres resource triggers the TransformationPipeline™", func() {
				applyResourceRequest(POSTGRES_RESOURCE_REQUEST)

				expectedName := types.NamespacedName{
					Name:      "postgres-promise-default-default-database",
					Namespace: "default",
				}
				Eventually(func() bool {
					return hasResourceBeenApplied(work_gvk, expectedName)
				}, timeout, interval).Should(BeTrue())
			})

			It("should label the created Work resource with a target workload cluster", func() {
				//applyResourceRequest(POSTGRES_RESOURCE_REQUEST)
				Eventually(func() bool {
					expectedName := types.NamespacedName{
						Name:      "postgres-promise-default-default-database",
						Namespace: "default",
					}

					work := &platformv1alpha1.Work{}
					err := k8sClient.Get(context.Background(), expectedName, work)
					Expect(err).ToNot(HaveOccurred())

					return metav1.HasLabel(work.ObjectMeta, "cluster")
				}, timeout, interval).Should(BeTrue())
			})

			It("should write a Postgres resource to ONE Worker Clusters", func() {
				Eventually(func() bool {
					workloadNamespacedName := types.NamespacedName{
						Name:      "postgres-promise-default-default-database",
						Namespace: "default",
					}

					resourceName := "database"
					resourceKind := "Database"

					foundCluster1, _ := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_1)
					foundCluster2, _ := workerHasResource(workloadNamespacedName, resourceName, resourceKind, WORKER_CLUSTER_2)

					if foundCluster1 && foundCluster2 {
						return false
					}

					if !foundCluster1 && !foundCluster2 {
						return false
					}

					return true
				}, timeout, interval).Should(BeTrue())
			})
		})
	})
})

func registerWorkerCluster(clusterName, clusterConfig string) {
	applyResourceRequest(clusterConfig)

	//defined in config/samples/platform_v1alpha1_worker_*_cluster.yaml
	expectedName := types.NamespacedName{
		Name:      clusterName,
		Namespace: "default",
	}
	Eventually(func() bool {
		return hasResourceBeenApplied(cluster_gvk, expectedName)
	}, timeout, interval).Should(BeTrue())
}

func getClusterConfigPath(clusterConfig string) string {
	yamlFile, err := ioutil.ReadFile(clusterConfig)
	Expect(err).ToNot(HaveOccurred())

	cluster := &platformv1alpha1.Cluster{}
	err = yaml.Unmarshal(yamlFile, cluster)
	Expect(err).ToNot(HaveOccurred())
	return cluster.Spec.BucketPath
}

func workerHasCRD(workloadNamespacedName types.NamespacedName, resourceName, resourceKind, clusterConfig string) (bool, unstructured.Unstructured) {
	objectName := "00-" + workloadNamespacedName.Namespace + "-" + workloadNamespacedName.Name + "-crds.yaml"
	bucketName := getClusterConfigPath(clusterConfig) + "-kratix-crds"
	return minioHasWorkloadWithResourceWithNameAndKind(bucketName, objectName, resourceName, resourceKind)
}

func workerHasResource(workloadNamespacedName types.NamespacedName, resourceName, resourceKind, clusterConfig string) (bool, unstructured.Unstructured) {
	objectName := "01-" + workloadNamespacedName.Namespace + "-" + workloadNamespacedName.Name + "-resources.yaml"
	bucketName := getClusterConfigPath(clusterConfig) + "-kratix-resources"
	return minioHasWorkloadWithResourceWithNameAndKind(bucketName, objectName, resourceName, resourceKind)
}

func minioHasWorkloadWithResourceWithNameAndKind(bucketName string, objectName string, resourceName string, resourceKind string) (bool, unstructured.Unstructured) {
	endpoint := "172.18.0.2:31337"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	Expect(err).ToNot(HaveOccurred())

	minioObject, err := minioClient.GetObject(context.Background(), bucketName, objectName, minio.GetObjectOptions{})
	Expect(err).ToNot(HaveOccurred())

	decoder := yaml.NewYAMLOrJSONDecoder(minioObject, 2048)

	ul := []unstructured.Unstructured{}
	for {
		us := unstructured.Unstructured{}
		err = decoder.Decode(&us)
		if err == io.EOF {
			//We reached the end of the file, move on to looking for the resource
			break
		} else if err != nil {
			/* There has been an error reading from Minio. It's likely that the
			   document has not been created in Minio yet, therefore we return
			   control to the ginkgo.Eventually to re-execute the assertions */
			return false, unstructured.Unstructured{}
		} else {
			//append the first resource to the resource slice, and go back through the loop
			ul = append(ul, us)
		}
	}

	for _, us := range ul {
		if us.GetKind() == resourceKind && us.GetName() == resourceName {
			//Hooray! we found the resource we're looking for!
			return true, us
		}
	}

	//We cannot find the resource and kind we are looking for
	return false, unstructured.Unstructured{}
}

//TODO Refactor this lot into own function. We can reuse this logic in controllers/suite_test.go
func hasResourceBeenApplied(gvk schema.GroupVersionKind, expectedName types.NamespacedName) bool {
	resource := &unstructured.Unstructured{}
	resource.SetGroupVersionKind(gvk)

	err := k8sClient.Get(context.Background(), expectedName, resource)
	return err == nil
}

func isAPIResourcePresent(gvk schema.GroupVersionKind) bool {
	_, err := k8sClient.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

func applyResourceRequest(filepath string) {
	yamlFile, err := ioutil.ReadFile(filepath)
	Expect(err).ToNot(HaveOccurred())

	request := &unstructured.Unstructured{}
	err = yaml.Unmarshal(yamlFile, request)
	Expect(err).ToNot(HaveOccurred())

	request.SetNamespace("default")
	err = k8sClient.Create(context.Background(), request)
	if !errors.IsAlreadyExists(err) {
		Expect(err).ToNot(HaveOccurred())
	}
}

func updateResourceRequest(filepath string) {
	yamlFile, err := ioutil.ReadFile(filepath)
	Expect(err).ToNot(HaveOccurred())

	request := &unstructured.Unstructured{}
	err = yaml.Unmarshal(yamlFile, request)
	Expect(err).ToNot(HaveOccurred())

	request.SetNamespace("default")

	currentResource := unstructured.Unstructured{}
	key := types.NamespacedName{
		Name:      request.GetName(),
		Namespace: request.GetNamespace(),
	}
	currentResource.SetGroupVersionKind(redis_gvk)

	err = k8sClient.Get(context.Background(), key, &currentResource)
	Expect(err).ToNot(HaveOccurred())

	//casting and stuff here
	currentResource.Object["spec"] = request.Object["spec"]
	err = k8sClient.Update(context.Background(), &currentResource)
	Expect(err).ToNot(HaveOccurred())
}

func applyPromiseCRD(filepath string) {
	promiseCR := &platformv1alpha1.Promise{}
	yamlFile, err := ioutil.ReadFile(filepath)
	Expect(err).NotTo(HaveOccurred())

	err = yaml.Unmarshal(yamlFile, promiseCR)
	Expect(err).ToNot(HaveOccurred())

	promiseCR.Namespace = "default"
	err = k8sClient.Create(context.Background(), promiseCR)
	if !errors.IsAlreadyExists(err) {
		Expect(err).ToNot(HaveOccurred())
	}
}

func isPodRunning(pod v1.Pod) bool {
	switch pod.Status.Phase {
	case v1.PodRunning:
		return true
	default:
		return false
	}
}

func getKratixControllerPod() v1.Pod {
	isController, _ := labels.NewRequirement("control-plane", selection.Equals, []string{"controller-manager"})
	selector := labels.NewSelector().
		Add(*isController)

	listOps := &client.ListOptions{
		Namespace:     "kratix-platform-system",
		LabelSelector: selector,
	}

	pods := &v1.PodList{}
	k8sClient.List(context.Background(), pods, listOps)
	if len(pods.Items) == 1 {
		return pods.Items[0]
	}
	return v1.Pod{}
}

func initK8sClient() {
	cfg := ctrl.GetConfigOrDie()

	err = platformv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
}
