package integration_test

import (
	"context"
	"io"
	"io/ioutil"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"

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
 1. A 'clean' k8s cluster
 2. `make deploy` has been run. Note: `make int-test` will
 ensure that `deploy` is executed
 3. `export IMG=syntasso/synpl-platform:dev make kind-load-image`
 4. `make int-test`

 Cleanup:
 k delete databases.postgresql.dev4devs.com database && k delete crd databases.postgresql.dev4devs.com && k delete promises.platform.synpl.syntasso.io postgres-promise && k delete works.platform.synpl.syntasso.io work-sample
*/
var (
	k8sClient client.Client
	err       error

	interval = "3s"
	timeout  = "120s"

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
		Group:   "platform.synpl.syntasso.io",
		Version: "v1alpha1",
		Kind:    "Work",
	}
)

const (
	REDIS_CRD                     = "../../config/samples/redis/redis-promise.yaml"
	REDIS_RESOURCE_REQUEST        = "../../config/samples/redis/redis-resource-request.yaml"
	REDIS_RESOURCE_UPDATE_REQUEST = "../../config/samples/redis/redis-resource-update-request.yaml"
	POSTGRES_CRD                  = "../../config/samples/postgres/postgres-promise.yaml"
	POSTGRES_RESOURCE_REQUEST     = "../../config/samples/postgres/postgres-resource-request.yaml"
)

var _ = Describe("SynplPlatform Integration Test", func() {
	BeforeSuite(func() {
		initK8sClient()

		By("SynPl is running")
		Eventually(func() bool {
			pod := getSynPlControllerPod()
			return isPodRunning(pod)
		}, timeout, interval).Should(BeTrue())
	})

	Describe("Redis Promise lifecycle", func() {
		It("Applying a Redis Promise CRD manifests a Redis api-resource", func() {
			applyPromiseCRD(REDIS_CRD)

			Eventually(func() bool {
				return isAPIResourcePresent(redis_gvk)
			}, timeout, interval).Should(BeTrue())
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
				//applyResourceRequest(REDIS_RESOURCE)
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

			It("should write a Redis resource to Minio", func() {
				// applyPromiseCRD(REDIS_CRD)
				// applyResourceRequest(REDIS_RESOURCE)
				Eventually(func() bool {
					workloadNamespacedName := types.NamespacedName{
						Name:      "redis-promise-default-default-opstree-redis",
						Namespace: "default",
					}

					//Read from Minio
					//Assert that the Postgres resource is present
					resourceName := "opstree-redis"
					resourceKind := "Redis"

					found, _ := minioHasWorkloadWithResourceWithNameAndKind(workloadNamespacedName, resourceName, resourceKind)
					return found
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
					//Assert that the Postgres resource is present
					resourceName := "opstree-redis"
					resourceKind := "Redis"

					found, obj := minioHasWorkloadWithResourceWithNameAndKind(workloadNamespacedName, resourceName, resourceKind)
					if found {
						spec := obj.Object["spec"]
						global := spec.(map[string]interface{})["global"]
						password := global.(map[string]interface{})["password"]
						return password == "Opstree@12345"
					}
					return false

				}, timeout, interval).Should(BeTrue())
			})
		})
	})

	Describe("Postgres Promise lifecycle", func() {
		It("Applying a Postgres Promise CRD manifests a Postgres api-resource", func() {
			applyPromiseCRD(POSTGRES_CRD)

			Eventually(func() bool {
				return isAPIResourcePresent(postgres_gvk)
			}, timeout, interval).Should(BeTrue())
		})

		Describe("Applying Postgres resource triggers the TransformationPipeline™", func() {
			It("Should have created a Work resource", func() {
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
				//applyResourceRequest(POSTGRES_RESOURCE)
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

			It("should write a Postgres resource to Minio", func() {
				// applyPromiseCRD(POSTGRES_CRD)
				// applyResourceRequest(POSTGRES_RESOURCE)
				Eventually(func() bool {
					workloadNamespacedName := types.NamespacedName{
						Name:      "postgres-promise-default-default-database",
						Namespace: "default",
					}

					//Read from Minio
					//Assert that the Postgres resource is present
					resourceName := "database"
					resourceKind := "Database"

					found, _ := minioHasWorkloadWithResourceWithNameAndKind(workloadNamespacedName, resourceName, resourceKind)
					return found
				}, timeout, interval).Should(BeTrue())
			})
		})
	})
})

func minioHasWorkloadWithResourceWithNameAndKind(workloadNamespacedName types.NamespacedName, resourceName string, resourceKind string) (bool, unstructured.Unstructured) {

	// endpoint := "minio.synpl-system.svc.cluster.local"
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

	bucketName := "synpl"
	// unique file name := "{workload metadata.name}-{workload metadata.namespace}"
	objectName := workloadNamespacedName.Namespace + "-" + workloadNamespacedName.Name + ".yaml"

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

func getSynPlControllerPod() v1.Pod {
	isController, _ := labels.NewRequirement("control-plane", selection.Equals, []string{"controller-manager"})
	selector := labels.NewSelector().
		Add(*isController)

	listOps := &client.ListOptions{
		Namespace:     "synpl-platform-system",
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
