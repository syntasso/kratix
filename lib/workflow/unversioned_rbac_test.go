package workflow_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO: remove soon, once users have upgraded to per-version pipeline RBAC.
var _ = Describe("RemoveUnversionedPipelineRBAC", func() {
	var (
		promise  v1alpha1.Promise
		pipeline v1alpha1.Pipeline
		rr       *unstructured.Unstructured
	)

	generate := func(version string) v1alpha1.PipelineJobResources {
		GinkgoHelper()
		resources, err := pipeline.ForResource(&promise, version, v1alpha1.WorkflowActionConfigure, rr).Resources(nil)
		Expect(err).NotTo(HaveOccurred())
		return resources
	}

	create := func(objects ...client.Object) {
		GinkgoHelper()
		for _, obj := range objects {
			Expect(fakeK8sClient.Create(ctx, obj)).To(Succeed())
		}
	}

	remove := func() {
		GinkgoHelper()
		Expect(workflow.RemoveUnversionedPipelineRBAC(ctx, fakeK8sClient, logger,
			[]v1alpha1.PipelineJobResources{generate("")})).To(Succeed())
	}

	exists := func(obj client.Object) bool {
		GinkgoHelper()
		err := fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
		if err != nil {
			Expect(err).To(MatchError(ContainSubstring("not found")))
			return false
		}
		return true
	}

	BeforeEach(func() {
		api, err := json.Marshal(fakeCRD)
		Expect(err).NotTo(HaveOccurred())
		promise = v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{Name: "redis"},
			Spec:       v1alpha1.PromiseSpec{API: &runtime.RawExtension{Raw: api}},
		}

		pipeline = v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "instance"},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
				RBAC: v1alpha1.RBAC{Permissions: []v1alpha1.Permission{
					{PolicyRule: rbacv1.PolicyRule{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"configmaps"}}},
					{ResourceNamespace: "specific-namespace", PolicyRule: rbacv1.PolicyRule{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}}},
					{ResourceNamespace: "*", PolicyRule: rbacv1.PolicyRule{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}}},
				}},
			},
		}

		rr = &unstructured.Unstructured{}
		rr.SetAPIVersion("mygroup.example/v1")
		rr.SetKind("TheKind")
		rr.SetName("example")
		rr.SetNamespace("default")
	})

	When("the RBAC from before the upgrade is still there", func() {
		var unversioned, stale, v1 []client.Object

		BeforeEach(func() {
			unversioned = rbacObjects(generate(""))

			// A ClusterRole for a namespace the pipeline declared before the upgrade but no longer does.
			staleClusterRole := generate("").Shared.ClusterRoles[0].DeepCopy()
			staleClusterRole.SetName("redis-resource-configure-instance-old-namespace-12345")
			staleClusterRole.Labels[v1alpha1.UserPermissionResourceNamespaceLabel] = "old-namespace"
			stale = []client.Object{staleClusterRole}

			v1 = rbacObjects(generate("v1.0.0"))
			configMap := generate("").Shared.ConfigMap

			create(unversioned...)
			create(stale...)
			create(v1...)
			create(configMap)
			v1 = append(v1, configMap)
		})

		It("deletes it when no pipeline from before the upgrade is running", func() {
			remove()

			for _, obj := range append(unversioned, stale...) {
				Expect(exists(obj)).To(BeFalse(), obj.GetName())
			}
			for _, obj := range v1 {
				Expect(exists(obj)).To(BeTrue(), obj.GetName())
			}
		})

		It("keeps it while a pipeline from before the upgrade is still running", func() {
			job := generate("").Job
			job.Status.Active = 1
			create(job)

			remove()

			for _, obj := range append(unversioned, stale...) {
				Expect(exists(obj)).To(BeTrue(), obj.GetName())
			}
		})

		It("deletes it while only versioned pipelines are running", func() {
			job := generate("v1.0.0").Job
			job.Status.Active = 1
			create(job)

			remove()

			for _, obj := range unversioned {
				Expect(exists(obj)).To(BeFalse(), obj.GetName())
			}
		})
	})

	It("keeps the RBAC of the not-set version, which uses the same names", func() {
		notSet := rbacObjects(generate(v1alpha1.UnversionedPromiseVersion))
		create(notSet...)

		remove()

		for _, obj := range notSet {
			Expect(exists(obj)).To(BeTrue(), obj.GetName())
		}
	})

	It("keeps a user-provided service account", func() {
		pipeline.Spec.RBAC.ServiceAccount = "custom"
		unversioned := rbacObjects(generate(""))
		create(unversioned...)

		remove()

		Expect(exists(generate("").Shared.ServiceAccount)).To(BeTrue())
		for _, obj := range unversioned[1:] {
			Expect(exists(obj)).To(BeFalse(), obj.GetName())
		}
	})

	It("keeps a service account Kratix did not create", func() {
		sa := generate("").Shared.ServiceAccount
		sa.SetLabels(nil)
		create(sa)

		remove()

		Expect(exists(sa)).To(BeTrue())
	})
})

func rbacObjects(resources v1alpha1.PipelineJobResources) []client.Object {
	objects := []client.Object{resources.Shared.ServiceAccount}
	for i := range resources.Shared.Roles {
		objects = append(objects, &resources.Shared.Roles[i])
	}
	for i := range resources.Shared.RoleBindings {
		objects = append(objects, &resources.Shared.RoleBindings[i])
	}
	for i := range resources.Shared.ClusterRoles {
		objects = append(objects, &resources.Shared.ClusterRoles[i])
	}
	for i := range resources.Shared.ClusterRoleBindings {
		objects = append(objects, &resources.Shared.ClusterRoleBindings[i])
	}
	return objects
}
