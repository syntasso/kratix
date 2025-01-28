package lib

import (
	"context"
	"fmt"
	"os"

	"github.com/syntasso/kratix/work-creator/pipeline/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
