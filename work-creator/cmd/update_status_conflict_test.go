package cmd_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/cmd"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/retry"
)

var _ = Describe("updateStatus when the object is modified concurrently", func() {
	for _, testCase := range []struct{ action, subresource string }{
		{"configure", ""}, {"configure", "status"}, {"delete", ""}, {"delete", "status"},
	} {
		action, subresource := testCase.action, testCase.subresource
		It(fmt.Sprintf("retries and keeps the concurrent write on a conflict (%s, subresource %q)", action, subresource), func() {
			GinkgoT().Setenv(v1alpha1.KratixActionEnvVar, action)
			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, "workflow-control.yaml"), []byte("retryAfter: 1m\nmessage: waiting\n"), 0o600)).To(Succeed())

			gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "databases"}
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.io/v1", "kind": "Database",
				"metadata": map[string]any{"name": "db", "namespace": "default"},
				"status":   map[string]any{"kratix": map[string]any{"workflows": map[string]any{action: map[string]any{"pipelines": []any{map[string]any{"name": "pipeline", "phase": "Running"}}}}}},
			}}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			attempts := 0
			client.PrependReactor("update", "databases", func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != subresource {
					return false, nil, nil
				}
				attempts++
				if attempts != 1 {
					return false, nil, nil
				}
				// Simulate an independent controller writing status while this update is in flight.
				latest, err := client.Tracker().Get(gvr, "default", "db")
				Expect(err).NotTo(HaveOccurred())
				concurrent := latest.(*unstructured.Unstructured).DeepCopy()
				Expect(unstructured.SetNestedField(concurrent.Object, "preserved", "status", "concurrent")).To(Succeed())
				Expect(client.Tracker().Update(gvr, concurrent, "default")).To(Succeed())
				return true, nil, apierrors.NewConflict(gvr.GroupResource(), "db", fmt.Errorf("concurrent update"))
			})
			resource := client.Resource(gvr).Namespace("default")
			params := &helpers.Parameters{ObjectName: "db", PipelineName: "pipeline", WorkflowType: v1alpha1.WorkflowTypeResource}

			Expect(cmd.UpdateStatus(context.Background(), dir, params, resource)).To(Succeed())

			stored, err := resource.Get(context.Background(), "db", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(attempts).To(Equal(2))
			Expect(stored.GetLabels()).To(HaveKeyWithValue(v1alpha1.WorkflowSuspendedLabel, "true"))
			concurrent, _, _ := unstructured.NestedString(stored.Object, "status", "concurrent")
			Expect(concurrent).To(Equal("preserved"))
			pipelines, _, _ := unstructured.NestedSlice(stored.Object, "status", "kratix", "workflows", action, "pipelines")
			Expect(pipelines).To(HaveLen(1))
			pipeline := pipelines[0].(map[string]any)
			Expect(pipeline).To(HaveKeyWithValue("phase", "Suspended"))
			Expect(pipeline).To(HaveKey("nextRetryAt"))
			Expect(pipeline).To(HaveKeyWithValue("attempts", int64(1)))
		})
	}

	It("gives up with an error when every update conflicts", func() {
		GinkgoT().Setenv(v1alpha1.KratixActionEnvVar, "configure")
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "workflow-control.yaml"), []byte("retryAfter: 1m\n"), 0o600)).To(Succeed())

		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "databases"}
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1", "kind": "Database",
			"metadata": map[string]any{"name": "db", "namespace": "default"},
		}}
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
		attempts := 0
		client.PrependReactor("update", "databases", func(action ktesting.Action) (bool, runtime.Object, error) {
			attempts++
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), "db", fmt.Errorf("concurrent update"))
		})
		params := &helpers.Parameters{ObjectName: "db", PipelineName: "pipeline", WorkflowType: v1alpha1.WorkflowTypeResource}

		err := cmd.UpdateStatus(context.Background(), dir, params, client.Resource(gvr).Namespace("default"))

		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(attempts).To(Equal(retry.DefaultRetry.Steps))
	})
})
