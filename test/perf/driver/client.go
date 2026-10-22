package driver

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// NewClient builds a controller-runtime client against the named kube context
// using the standard kubeconfig discovery rules (KUBECONFIG env var or
// ~/.kube/config). The kratix v1alpha1 types are registered so callers can
// work with Promise objects directly; dynamic RR CRs are accessed via
// unstructured.Unstructured.
func NewClient(kubeContext string) (client.Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		rules.ExplicitPath = kc
	} else if home, err := os.UserHomeDir(); err == nil {
		rules.ExplicitPath = filepath.Join(home, ".kube", "config")
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig (context=%s): %w", kubeContext, err)
	}
	// crank QPS so 2500 parallel creates aren't client-side throttled
	cfg.QPS = 200
	cfg.Burst = 400

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}
