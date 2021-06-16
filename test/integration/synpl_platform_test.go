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
* Run these tests using `make int-test` to ensure that the correct resources are applied
* to the k8s cluster under test.
*
* Assumptions:
* 1. A 'clean' k8s cluster
* 2. `make deploy` has been run. Note: `make int-test` will
* ensure that `deploy` is executed
 */
var (
	k8sClient client.Client
	err       error

	interval = "3s"
	timeout  = "35s"
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

	It("Applying a Redis Promise CRD manifests a Redis api-resource", func() {
		applyRedisPromiseCRD()

		Eventually(func() bool {
			return isRedisAPIResourcePresent()
		}, timeout, interval).Should(BeTrue())
	})

	It("Applying Redis resource triggers the TransformationPipeline™", func() {
		applyRedisResourceRequest()

		Eventually(func() bool {
			return isSyntassoAnnotationApplied()
		}, timeout, interval).Should(BeTrue())
	})
})

//TODO Refactor this lot into own function. We can reuse this logic in controllers/suite_test.go
func isSyntassoAnnotationApplied() bool {
	gvk := schema.GroupVersionKind{
		Group:   "redis.redis.opstreelabs.in",
		Version: "v1beta1",
		Kind:    "Redis",
	}
	redisRequest := &unstructured.Unstructured{}
	redisRequest.SetGroupVersionKind(gvk)

	expectedName := types.NamespacedName{
		Name:      "opstree-redis",
		Namespace: "default",
	}

	err := k8sClient.Get(context.Background(), expectedName, redisRequest)
	Expect(err).ToNot(HaveOccurred())

	annotations := redisRequest.GetAnnotations()

	if val, ok := annotations["syntasso"]; ok {
		return val == "true"
	}

	return false
}

func isRedisAPIResourcePresent() bool {
	gvk := schema.GroupVersionKind{
		Group:   "redis.redis.opstreelabs.in",
		Version: "v1beta1",
		Kind:    "Redis",
	}
	_, err := k8sClient.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

func applyRedisResourceRequest() {
	yamlFile, err := ioutil.ReadFile("../../config/samples/redis/redis-resource-request.yaml")
	Expect(err).ToNot(HaveOccurred())

	redisRequest := &unstructured.Unstructured{}
	err = yaml.Unmarshal(yamlFile, redisRequest)
	Expect(err).ToNot(HaveOccurred())

	redisRequest.SetNamespace("default")
	err = k8sClient.Create(context.Background(), redisRequest)
	Expect(err).ToNot(HaveOccurred())
}

func applyRedisPromiseCRD() {
	promiseCR := &platformv1alpha1.Promise{}
	yamlFile, err := ioutil.ReadFile("../../config/samples/redis/redis-promise.yaml")
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
