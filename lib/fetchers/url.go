package fetchers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type URLFetcher struct {
}

func (u *URLFetcher) FromURL(urlString, authHeader string) (*v1alpha1.Promise, error) {
	req, err := http.NewRequest("GET", urlString, nil)
	if err != nil {
		return nil, err
	}

	if authHeader != "" {
		req.Header.Add("Authorization", authHeader)
	}

	client := &http.Client{
		Timeout: time.Second * 10,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get Promise from URL: status code %d", resp.StatusCode)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(resp.Body, 2048)

	// Use an unstructured k8s object to perform basic validation that the YAML is a k8s
	// object.
	unstructuredPromise := unstructured.Unstructured{}

	var promise v1alpha1.Promise

	if err := decoder.Decode(&unstructuredPromise); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into Kubernetes object: %s", err.Error())
	}

	if kind := unstructuredPromise.GetKind(); kind != "Promise" {
		return nil, fmt.Errorf("expected single Promise object but found object of kind: %s", kind)
	}

	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredPromise.Object, &promise)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal into Promise: %w", err)
	}

	// Attempt to decode again to check if there are multiple objects.
	if err = decoder.Decode(&unstructuredPromise); err == nil {
		return nil, fmt.Errorf("expected single document yaml, but found multiple documents")
	}

	return &promise, nil
}
