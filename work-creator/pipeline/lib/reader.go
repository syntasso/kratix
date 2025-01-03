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
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	if err := r.writeObjectToFile(ctx, client, params); err != nil {
		return err
	}

	if params.Healthcheck {
		if err := r.writePromiseToFile(ctx, client, params); err != nil {
			return err
		}
	}

	return nil
}

func (r *Reader) writeObjectToFile(ctx context.Context, client dynamic.Interface, params *helpers.Parameters) error {
	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	obj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get object: %v", err)
	}

	objectFilePath := params.GetObjectPath()
	if err := helpers.WriteToYaml(obj, objectFilePath); err != nil {
		return fmt.Errorf("failed to write object to file: %v", err)
	}

	fmt.Fprintln(r.Out, "Object file written to:", objectFilePath, "head of file:")
	if err := helpers.PrintFileHead(r.Out, objectFilePath, 500); err != nil {
		return fmt.Errorf("failed to print file head: %v", err)
	}

	return nil
}

func (r *Reader) writePromiseToFile(ctx context.Context, client dynamic.Interface, params *helpers.Parameters) error {
	promiseClient := client.Resource(helpers.PromiseGVR())

	obj, err := promiseClient.Get(ctx, params.PromiseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get Promise: %v", err)
	}

	promiseFilePath := params.GetPromisePath()
	if err := helpers.WriteToYaml(obj, promiseFilePath); err != nil {
		return fmt.Errorf("failed to write Promise to file: %v", err)
	}

	fmt.Fprintln(r.Out, "Promise file written to:", promiseFilePath, "head of file:")
	if err := helpers.PrintFileHead(r.Out, promiseFilePath, 500); err != nil {
		return fmt.Errorf("failed to print file head: %v", err)
	}

	return nil
}
