package lib

import (
	"context"
	"fmt"
	"os"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

type Reader struct {
	Out *os.File
}

func (r *Reader) Run(ctx context.Context) error {
	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	if err := r.writeObjectToFile(ctx, client, params); err != nil {
		return err
	}

	return nil
}

func (r *Reader) writeObjectToFile(ctx context.Context, client dynamic.Interface, params *helpers.Parameters) error {
	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	obj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
	}

	obj = r.resolveObject(ctx, client, params, obj)

	objectFilePath := params.GetObjectPath()
	if err := helpers.WriteToYaml(obj, objectFilePath); err != nil {
		return fmt.Errorf("failed to write object to file: %w", err)
	}

	fmt.Fprintln(r.Out, "Object file written to:", objectFilePath, "head of file:")
	if err := helpers.PrintFileHead(r.Out, objectFilePath, 500); err != nil {
		return fmt.Errorf("failed to print file head: %w", err)
	}

	return nil
}

// resolveObject returns the real resource when running inside a dry-run pipeline.
// The ephemeral RR carries kratix.io/dry-run-resource-name and
// kratix.io/dry-run-resource-namespace annotations; if present and the resource
// exists, the pipeline sees the real object's metadata instead of the ephemeral
// RR's generated name. Falls back to the ephemeral RR for new requests.
func (r *Reader) resolveObject(ctx context.Context, client dynamic.Interface, params *helpers.Parameters, ephemeral *unstructured.Unstructured) *unstructured.Unstructured {
	if os.Getenv(v1alpha1.KratixDryRunEnvVar) != "true" {
		return ephemeral
	}

	annotations := ephemeral.GetAnnotations()
	realName := annotations[v1alpha1.DryRunResourceNameAnnotation]
	if realName == "" {
		return ephemeral
	}

	realNamespace := annotations[v1alpha1.DryRunResourceNamespaceAnnotation]
	realObj, err := client.Resource(helpers.ObjectGVR(params)).Namespace(realNamespace).Get(ctx, realName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// New request — real resource doesn't exist yet; use the ephemeral RR as-is.
			fmt.Fprintln(r.Out, "dry-run: real resource not found, using ephemeral RR")
			return ephemeral
		}
		// Unexpected error: log and fall back rather than failing the pipeline.
		fmt.Fprintf(r.Out, "dry-run: failed to fetch real resource %s/%s: %v; using ephemeral RR\n", realNamespace, realName, err)
		return ephemeral
	}

	// Use real resource metadata so the pipeline sees the correct name/namespace/labels,
	// but apply the proposed spec from the ephemeral RR so the diff reflects the change.
	merged := realObj.DeepCopy()
	merged.Object["spec"] = ephemeral.Object["spec"]
	fmt.Fprintf(r.Out, "dry-run: using real resource %s/%s metadata with proposed spec\n", realNamespace, realName)
	return merged
}
