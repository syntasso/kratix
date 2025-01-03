package lib

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/work-creator/pipeline/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

type Reader struct{}

func (r *Reader) Run() error {
	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	if err := writeObjectToFile(client, params); err != nil {
		return err
	}

	if params.Healthcheck {
		if err := writePromiseToFile(client, params); err != nil {
			return err
		}
	}

	return nil
}

func writeObjectToFile(client dynamic.Interface, params *helpers.Parameters) error {
	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	obj, err := objectClient.Get(context.TODO(), params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get object: %v", err)
	}

	objectFilePath := params.GetObjectPath()
	if err := helpers.WriteToYaml(obj, objectFilePath); err != nil {
		return fmt.Errorf("failed to write object to file: %v", err)
	}

	return nil
}

func writePromiseToFile(client dynamic.Interface, params *helpers.Parameters) error {
	promiseClient := client.Resource(helpers.PromiseGVR())

	obj, err := promiseClient.Get(context.TODO(), params.PromiseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get Promise: %v", err)
	}

	promiseFilePath := params.GetPromisePath()
	if err := helpers.WriteToYaml(obj, promiseFilePath); err != nil {
		return fmt.Errorf("failed to write Promise to file: %v", err)
	}

	return nil
}
