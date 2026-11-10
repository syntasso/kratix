package cmd_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
)

func TestUpdateStatusSuspensionConflict(t *testing.T) {
	for _, subresource := range []string{"", "status"} {
		t.Run("conflict-"+subresource, func(t *testing.T) {
			t.Setenv(v1alpha1.KratixActionEnvVar, "configure")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "workflow-control.yaml"), []byte("retryAfter: 1m\nmessage: waiting\n"), 0600); err != nil {
				t.Fatal(err)
			}
			gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "databases"}
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.io/v1", "kind": "Database",
				"metadata": map[string]any{"name": "db", "namespace": "default"},
				"status":   map[string]any{"kratix": map[string]any{"workflows": map[string]any{"configure": map[string]any{"pipelines": []any{map[string]any{"name": "pipeline", "phase": "Running"}}}}}},
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
				if err != nil {
					t.Fatal(err)
				}
				concurrent := latest.(*unstructured.Unstructured).DeepCopy()
				_ = unstructured.SetNestedField(concurrent.Object, "preserved", "status", "concurrent")
				if err := client.Tracker().Update(gvr, concurrent, "default"); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewConflict(gvr.GroupResource(), "db", fmt.Errorf("concurrent update"))
			})
			resource := client.Resource(gvr).Namespace("default")
			params := &helpers.Parameters{ObjectName: "db", PipelineName: "pipeline", WorkflowType: v1alpha1.WorkflowTypeResource}
			if err := cmd.UpdateStatus(context.Background(), dir, params, resource); err != nil {
				t.Fatal(err)
			}
			stored, err := resource.Get(context.Background(), "db", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
			if stored.GetLabels()[v1alpha1.WorkflowSuspendedLabel] != "true" {
				t.Fatal("missing suspension label")
			}
			value, _, _ := unstructured.NestedString(stored.Object, "status", "concurrent")
			if value != "preserved" {
				t.Fatal("lost concurrent status")
			}
			pipelines, _, _ := unstructured.NestedSlice(stored.Object, "status", "kratix", "workflows", "configure", "pipelines")
			pipeline := pipelines[0].(map[string]any)
			if pipeline["phase"] != "Suspended" || pipeline["nextRetryAt"] == nil || pipeline["attempts"] != int64(1) {
				t.Fatalf("unexpected pipeline status: %v", pipeline)
			}
		})
	}
}
