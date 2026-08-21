package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// MetricsScraperSA is the ServiceAccount the perf rig uses to scrape the
	// controller's secure metrics endpoint. It lives in the kratix platform
	// namespace and is bound to the kratix-platform-metrics-reader
	// ClusterRole (a non-resource URL grant on /metrics).
	MetricsScraperSA  = "perf-metrics-scraper"
	MetricsScraperRB  = "perf-metrics-scraper"
	MetricsReaderRole = "kratix-platform-metrics-reader"
)

// EnsureMetricsScraperRBAC creates the ServiceAccount and ClusterRoleBinding
// the scraper needs, then mints a short-lived token for the SA via
// TokenRequest. Returns the bearer token.
func EnsureMetricsScraperRBAC(ctx context.Context, kubeContext, namespace string, tokenTTL time.Duration) (string, error) {
	cs, err := newClientset(kubeContext)
	if err != nil {
		return "", err
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: MetricsScraperSA, Namespace: namespace},
	}
	if _, err := cs.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create sa: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: MetricsScraperRB},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: MetricsScraperSA, Namespace: namespace},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     MetricsReaderRole,
		},
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create crb: %w", err)
	}

	ttl := int64(tokenTTL.Seconds())
	tr := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{ExpirationSeconds: &ttl},
	}
	out, err := cs.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, MetricsScraperSA, tr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	return out.Status.Token, nil
}

func newClientset(kubeContext string) (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		rules.ExplicitPath = kc
	} else if home, err := os.UserHomeDir(); err == nil {
		rules.ExplicitPath = filepath.Join(home, ".kube", "config")
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}
