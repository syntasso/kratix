package lib

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	helmclient "github.com/mittwald/go-helm-client"
	"github.com/mittwald/go-helm-client/values"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/repo"

	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var (
	chartName        = "cert-manager"
	repoURL          = "https://charts.jetstack.io"
	repoName         = "jetstack"
	helmClient       helmclient.Client
	KratixInstallURL = GetEnv("KRATIX_INSTALL_URL", "https://github.com/syntasso/kratix/releases/latest/download/install-all-in-one.yaml")
	KratixConfigURL  = GetEnv("KRATIX_CONFIG_URL", "https://github.com/syntasso/kratix/releases/latest/download/config-all-in-one.yaml")
)

func GetEnv(envKey, fallback string) string {
	if val := os.Getenv(envKey); val != "" {
		return val
	}
	return fallback
}

// Step 1: Install cert-manager and wait for its pods to be ready
func InstallCertManager(ctx context.Context, step, totalSteps int) error {
	fmt.Printf("\n🔹 Step %d/%d: Installing cert-manager\n", step, totalSteps)

	hClient, err := getHelmClient()
	if err != nil {
		return fmt.Errorf("could not generate helm client: %w", err)
	}

	chart, _, err := hClient.GetChart(chartName, &action.ChartPathOptions{RepoURL: repoURL})
	if err != nil {
		return fmt.Errorf("could not get cert-manager helm chart %s from %s: %v", chartName, repoURL, err)
	}

	if err = hClient.AddOrUpdateChartRepo(repo.Entry{
		Name: repoName,
		URL:  repoURL,
	}); err != nil {
		return fmt.Errorf("could not add chart repo: %s with error: %v", repoURL, err)
	}

	fmt.Println("JetStack helm chart registry '%s' added.\n", repoURL)
	fmt.Println("Installing cert-manager helm chart\n")

	spec := &helmclient.ChartSpec{
		ReleaseName: chartName,
		ChartName:   fmt.Sprintf("%s/%s", repoName, chartName),
		Namespace:   "cert-manager",
		ValuesOptions: values.Options{
			Values: []string{
				"crds.enabled=true",
				"serviceAccount.create=true",
				"serviceAccount.name=cert-manager",
				"global.leaderElection.namespace=cert-manager",
			},
		},
		Version:         chart.Metadata.Version,
		CreateNamespace: true,
		Wait:            true,
		Timeout:         1 * time.Minute,
	}

	if _, err = hClient.InstallChart(context.Background(), spec, nil); err != nil {
		return fmt.Errorf("could not install chart: %v", err)
	}

	fmt.Println("  └─ Waiting for cert-manager pods to become Ready...")
	if err := WaitForPod(ctx, "cert-manager", "app.kubernetes.io/component=controller"); err != nil {
		return err
	}
	if err := WaitForPod(ctx, "cert-manager", "app.kubernetes.io/component=cainjector"); err != nil {
		return err
	}
	return WaitForPod(ctx, "cert-manager", "app.kubernetes.io/component=webhook")
}

// Step 2: Install Kratix platform core
func InstallKratix(ctx context.Context, step, totalSteps int) error {
	fmt.Printf("\n🔹 Step %d/%d: Installing Kratix\n", step, totalSteps)
	if err := KubectlWithRetry(ctx, "apply", "--filename", KratixInstallURL); err != nil {
		return fmt.Errorf("applying kratix: %w", err)
	}

	fmt.Println("  └─ Waiting for kratix-platform pods to become Ready...")
	return WaitForPod(ctx, "kratix-platform-system", "app.kubernetes.io/instance=kratix-platform")
}

// Step 3: Configure Kratix with destinations, bucket, kustomizations
func ConfigureKratix(ctx context.Context, step, totalSteps int) error {
	fmt.Printf("\n🔹 Step %d/%d: Configuring Kratix\n", step, totalSteps)
	if err := KubectlWithRetry(ctx, "apply", "--filename", KratixConfigURL); err != nil {
		return fmt.Errorf("applying config: %w", err)
	}

	fmt.Println("  └─ Waiting for namespace 'kratix-worker-system' to appear...")
	return WaitForNamespace(ctx, "kratix-worker-system")
}

// Step 4: Final confirmation
func FinalizeInstall(totalSteps int) {
	fmt.Printf("\n🔹 Step %d/%d: Verifying install complete\n", totalSteps, totalSteps)
	fmt.Println("  └─ All systems are up!")
	fmt.Println("✅ Kratix installation complete!")
}

func KubectlWithRetry(ctx context.Context, args ...string) error {
	_, err := kubectlWithRetry(ctx, true, args...)

	return err
}

func KubectlWithRetryOutputOnly(ctx context.Context, args ...string) (string, error) {
	return kubectlWithRetry(ctx, false, args...)
}

func kubectlWithRetry(ctx context.Context, outputToStd bool, args ...string) (string, error) {
	const maxRetries = 5

	var combinedOutput bytes.Buffer

	for i := 1; i <= maxRetries; i++ {
		cmd := exec.CommandContext(ctx, "kubectl", args...)

		// Use a multiwriter to stream output to both the terminal and a buffer
		if outputToStd {
			stdoutWriter := io.MultiWriter(os.Stdout, &combinedOutput)
			stderrWriter := io.MultiWriter(os.Stderr, &combinedOutput)
			cmd.Stdout = stdoutWriter
			cmd.Stderr = stderrWriter
		} else {
			cmd.Stdout = &combinedOutput
			cmd.Stderr = &combinedOutput
		}

		err := cmd.Run()
		if err == nil {
			return combinedOutput.String(), nil
		}

		fmt.Printf("  ⚠️  kubectl failed (attempt %d/%d): %v\n", i, maxRetries, err)
		time.Sleep(10 * time.Second)
	}

	return combinedOutput.String(), fmt.Errorf("command failed after %d retries: kubectl %s", maxRetries, strings.Join(args, " "))
}

func WaitForPod(ctx context.Context, namespace, labelSelector string) error {
	return KubectlWithRetry(ctx, "wait", "--for=condition=Ready", "pod",
		"-l", labelSelector,
		"-n", namespace,
		"--timeout=180s")
}

func WaitForNamespace(ctx context.Context, ns string) error {
	for i := 0; i < 30; i++ {
		cmd := exec.CommandContext(ctx, "kubectl", "get", "namespace", ns)
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("namespace '%s' did not appear in time", ns)
}

func getHelmClient() (helmclient.Client, error) {
	if helmClient != nil {
		return helmClient, nil
	}

	cfg, err := config.GetConfig()
	if err != nil {
		return nil, err
	}

	hClient, err := helmclient.NewClientFromRestConf(&helmclient.RestConfClientOptions{
		RestConfig: cfg,
		Options:    &helmclient.Options{Namespace: "default"}})
	if err != nil {
		return nil, err
	}

	return hClient, nil
}
