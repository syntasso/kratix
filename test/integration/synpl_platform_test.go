package integration_test

import (
	"context"
	"io/ioutil"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"

	v1 "k8s.io/api/core/v1"

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
*/
var (
	k8sClient client.Client
	err       error

	interval = "3s"
	timeout  = "35s"

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
	REDIS_CRD         = "../../config/samples/redis/redis-promise.yaml"
	REDIS_RESOURCE    = "../../config/samples/redis/redis-resource-request.yaml"
	POSTGRES_CRD      = "../../config/samples/postgres/postgres-promise.yaml"
	POSTGRES_RESOURCE = "../../config/samples/postgres/postgres-resource-request.yaml"
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

	Context("Redis", func() {
		It("Applying a Redis Promise CRD manifests a Redis api-resource", func() {
			applyPromiseCRD(REDIS_CRD)

			Eventually(func() bool {
				return isAPIResourcePresent(redis_gvk)
			}, timeout, interval).Should(BeTrue())
		})

		It("Applying Redis resource triggers the TransformationPipeline™", func() {
			applyResourceRequest(REDIS_RESOURCE)

			expectedName := types.NamespacedName{
				Name:      "opstree-redis",
				Namespace: "default",
			}
			Eventually(func() bool {
				return hasResourceBeenApplied(redis_gvk, expectedName)
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("Postgres", func() {
		It("Applying a Postgres Promise CRD manifests a Postgres api-resource", func() {
			applyPromiseCRD(POSTGRES_CRD)

			Eventually(func() bool {
				return isAPIResourcePresent(postgres_gvk)
			}, timeout, interval).Should(BeTrue())
		})

		It("Applying Postgres resource triggers the TransformationPipeline™", func() {
			applyResourceRequest(POSTGRES_RESOURCE)

			expectedName := types.NamespacedName{
				Name:      "work-sample",
				Namespace: "default",
			}
			Eventually(func() bool {
				return hasResourceBeenApplied(work_gvk, expectedName)
			}, timeout, interval).Should(BeTrue())
		})
	})
})

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
	Expect(err).ToNot(HaveOccurred())
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
