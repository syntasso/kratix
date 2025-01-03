package helpers

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

var GetParametersFromEnv = getParametersFromEnv
var GetK8sClient = getK8sClient

type Parameters struct {
	ObjectGroup     string
	ObjectName      string
	ObjectVersion   string
	ObjectNamespace string
	PromiseName     string

	CRDPlural      string
	ClusterScoped  bool
	IsLastPipeline bool
	Healthcheck    bool

	InputDir  string
	OutputDir string
}

func getParametersFromEnv() *Parameters {
	p := &Parameters{
		ObjectGroup:     os.Getenv("OBJECT_GROUP"),
		ObjectName:      os.Getenv("OBJECT_NAME"),
		ObjectVersion:   os.Getenv("OBJECT_VERSION"),
		ObjectNamespace: os.Getenv("OBJECT_NAMESPACE"),
		PromiseName:     os.Getenv("PROMISE_NAME"),
		CRDPlural:       os.Getenv("CRD_PLURAL"),
		ClusterScoped:   os.Getenv("CLUSTER_SCOPED") == "true",
		IsLastPipeline:  os.Getenv("IS_LAST_PIPELINE") == "true",
		Healthcheck:     os.Getenv("HEALTHCHECK") == "true",
		InputDir:        os.Getenv("INPUT_DIR"),
		OutputDir:       os.Getenv("OUTPUT_DIR"),
	}

	if p.InputDir == "" {
		p.InputDir = "/kratix/input"
	}
	if p.OutputDir == "" {
		p.OutputDir = "/kratix/output"
	}
	if p.ObjectNamespace == "" {
		p.ObjectNamespace = "default"
	}
	if p.ClusterScoped {
		p.ObjectNamespace = ""
	}

	return p
}

func (p *Parameters) GetPromisePath() string {
	return filepath.Join(p.InputDir, "promise.yaml")
}

func (p *Parameters) GetObjectPath() string {
	return filepath.Join(p.InputDir, "object.yaml")
}

// GetK8sClient creates a Kubernetes client with the given scheme
func getK8sClient() (dynamic.Interface, error) {
	// Try to load in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}

	return dynamic.NewForConfig(config)
}

func WriteToYaml(obj interface{}, objectFilePath string) error {
	objYAML, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %v", err)
	}

	if err := os.WriteFile(objectFilePath, objYAML, 0644); err != nil {
		return fmt.Errorf("failed to write object to file: %v", err)
	}
	return nil
}

func ObjectGVR(params *Parameters) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    params.ObjectGroup,
		Version:  params.ObjectVersion,
		Resource: params.CRDPlural,
	}
}

func PromiseGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: "promises",
	}
}
