//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Default target to run when 'mage' is called without arguments.
var Default = Install

// --- Configuration Defaults ---
var (
	PlatformCluster = getEnv("PLATFORM_CLUSTER_NAME", "platform")
	Worker1Cluster  = getEnv("WORKER1_CLUSTER_NAME", "worker")
	Worker2Cluster  = getEnv("WORKER2_CLUSTER_NAME", "worker-2")
	KindImage       = getEnv("KIND_IMAGE", "kindest/node:v1.33.1")
	Version         = getEnv("VERSION", "dev")
	WaitTimeout     = getEnv("WAIT_TIMEOUT", "180s")

	Root, _        = os.Getwd()
	PlatformConfig = getEnv("KIND_PLATFORM_CONFIG", filepath.Join(Root, "hack/platform/kind-platform-config.yaml"))
	WorkerConfig   = getEnv("KIND_WORKER_CONFIG", filepath.Join(Root, "hack/destination/kind-worker-config.yaml"))

	Recreate      = getEnvBool("RECREATE", false)
	SingleCluster = getEnvBool("SINGLE_CLUSTER", false)
	ThirdCluster  = getEnvBool("THIRD_CLUSTER", false)
	UseGit        = getEnvBool("USE_GIT", false)
	GitAndMinio   = getEnvBool("GIT_AND_MINIO", false)
	NoCertManager = getEnvBool("NO_CERT_MANAGER", false)
)

// --- Primary Targets ---

// Install performs the full Kratix quickstart following the exact bash script sequence.
// Sequence: Parallel(Build/Clusters) -> Load -> Infra Setup -> Platform Controller -> Registration.
func Install() {
	mg.SerialDeps(Verify, Clean)
	fmt.Println("🚀 Starting parallel build and cluster creation...")

	// 1. Parallel Block
	mg.Deps(BuildKratixImage, CreatePlatform, CreateWorker1, CreateWorker2)

	// 2. Sequential Setup
	mg.SerialDeps(LoadKratixImage, SetupInfrastructure, SetupPlatformController, WaitForWebhookReady, RegisterAllDestinations, WaitForSystem)

	cleanupJobs()
	fmt.Println("✅ Kratix installation complete!")
}

// Clean deletes existing KinD clusters if RECREATE=true.
func Clean() {
	if !Recreate {
		return
	}
	fmt.Println("🧹 Deleting pre-existing clusters...")
	sh.Run("kind", "delete", "clusters", PlatformCluster, Worker1Cluster)
	if ThirdCluster {
		sh.Run("kind", "delete", "cluster", "--name", Worker2Cluster)
	}
}

// Verify checks for required binaries (kind, kubectl, docker, yq, make).
func Verify() error {
	bins := []string{"kind", "kubectl", "docker", "yq", "make"}
	for _, b := range bins {
		if _, err := sh.Output("which", b); err != nil {
			return fmt.Errorf("%s not found in PATH", b)
		}
	}
	return nil
}

// --- Parallel Targets ---

func BuildKratixImage() error {
	if Version == "dev" {
		sh.RunV("make", "distribution")
	}
	image := fmt.Sprintf("syntasso/kratix-platform:%s", Version)
	fmt.Printf("📦 Building %s...\n", image)
	return sh.RunV("docker", "build", "-t", image, ".")
}

func CreatePlatform() error {
	fmt.Printf("🏗️  Creating %s cluster...\n", PlatformCluster)
	return sh.RunV("kind", "create", "cluster", "--name", PlatformCluster, "--image", KindImage, "--config", PlatformConfig)
}

func CreateWorker1() error {
	if SingleCluster {
		return nil
	}
	fmt.Printf("🏗️  Creating %s cluster...\n", Worker1Cluster)
	return sh.RunV("kind", "create", "cluster", "--name", Worker1Cluster, "--image", KindImage, "--config", WorkerConfig)
}

func CreateWorker2() error {
	if !ThirdCluster {
		return nil
	}
	fmt.Printf("🏗️  Creating %s cluster...\n", Worker2Cluster)
	cfg := filepath.Join(Root, "config/samples/kind-worker-2-config.yaml")
	return sh.RunV("kind", "create", "cluster", "--name", Worker2Cluster, "--image", KindImage, "--config", cfg)
}

// --- Setup Targets ---

func LoadKratixImage() error {
	image := fmt.Sprintf("syntasso/kratix-platform:%s", Version)
	return sh.RunV("kind", "load", "docker-image", image, "--name", PlatformCluster)
}

func SetupInfrastructure() {
	ctx := "kind-" + PlatformCluster
	if !NoCertManager {
		sh.RunV("kubectl", "--context", ctx, "apply", "-f", "https://github.com/cert-manager/cert-manager/releases/download/v1.15.0/cert-manager.yaml")
		sh.RunV("kubectl", "--context", ctx, "wait", "-n", "cert-manager", "--for=condition=available", "deployment/cert-manager-webhook", "--timeout", WaitTimeout)
		sh.RunV("kubectl", "--context", ctx, "wait", "-n", "cert-manager", "--for=condition=Ready", "pods", "--selector=app.kubernetes.io/component=webhook", "--timeout", WaitTimeout)
	}
	if UseGit || GitAndMinio {
		mg.SerialDeps(SetupGitea)
	}
	if !UseGit || GitAndMinio {
		mg.SerialDeps(SetupMinio)
	}
}

func SetupGitea() error {
	ctx := "kind-" + PlatformCluster
	sh.RunV("make", "gitea-cli")
	ip, _ := sh.Output("docker", "inspect", PlatformCluster+"-control-plane", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	sh.RunV("./bin/gitea", "cert", "--host", ip, "--ca")
	sh.RunV("kubectl", "--context", ctx, "create", "ns", "gitea")
	sh.RunV("kubectl", "--context", ctx, "apply", "-f", filepath.Join(Root, "hack/platform/gitea-install.yaml"))
	sh.RunV("kubectl", "wait", "pod", "-n", "gitea", "--selector=app=gitea", "--for=condition=ready", "--context", ctx, "--timeout", WaitTimeout)
	return sh.RunV("kubectl", "wait", "job", "-n", "gitea", "gitea-create-repository", "--for=condition=complete", "--context", ctx, "--timeout", WaitTimeout)
}

func SetupMinio() error {
	ctx := "kind-" + PlatformCluster
	sh.RunV("make", "minio-cli")
	sh.RunV("kubectl", "--context", ctx, "apply", "-f", filepath.Join(Root, "hack/platform/minio-install.yaml"))
	sh.RunV("kubectl", "wait", "pod", "-n", "kratix-platform-system", "--selector=run=minio", "--for=condition=ready", "--context", ctx, "--timeout", WaitTimeout)
	return sh.RunV("kubectl", "wait", "job", "minio-create-bucket", "-n", "default", "--for=condition=complete", "--context", ctx, "--timeout", WaitTimeout)
}

func SetupPlatformController() error {
	ctx := "kind-" + PlatformCluster
	fmt.Println("📦 Applying Kratix Controller...")
	return retry(5, 5*time.Second, func() error {
		return sh.RunV("kubectl", "--context", ctx, "apply", "-f", "distribution/kratix.yaml")
	})
}

func WaitForWebhookReady() error {
	ctx := "kind-" + PlatformCluster
	sh.RunV("kubectl", "--context", ctx, "wait", "deployment", "-n", "kratix-platform-system", "kratix-platform-controller-manager", "--for=condition=Available", "--timeout", WaitTimeout)
	return sh.RunV("kubectl", "--context", ctx, "wait", "pod", "-n", "kratix-platform-system", "--selector=control-plane=controller-manager", "--for=condition=Ready", "--timeout", WaitTimeout)
}

// --- Registration Phase ---

// RegisterAllDestinations applies registration logic found in the original register-destination scripts.
func RegisterAllDestinations() {
	platformCtx := "kind-" + PlatformCluster
	platformIP, _ := sh.Output("docker", "inspect", PlatformCluster+"-control-plane", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")

	// 1. Register StateStores
	fmt.Println("🔗 Registering StateStore...")
	if UseGit {
		content, _ := os.ReadFile(filepath.Join(Root, "config/samples/platform_v1alpha1_gitstatestore.yaml"))
		patched := strings.ReplaceAll(string(content), "172.18.0.2", platformIP)
		retry(5, 5*time.Second, func() error { return pipeToKubectl(patched, platformCtx) })
	} else {
		sh.RunV("kubectl", "--context", platformCtx, "apply", "-f", filepath.Join(Root, "config/samples/platform_v1alpha1_bucketstatestore.yaml"))
		sh.RunV("kubectl", "--context", platformCtx, "wait", "bucketstatestore", "default", "--for=condition=Ready")
	}

	// 2. Register Destinations
	if SingleCluster {
		registerDestination("platform-cluster", platformCtx, platformCtx, platformIP)
	} else {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			registerDestination("worker-1", "kind-"+Worker1Cluster, platformCtx, platformIP)
		}()
		if ThirdCluster {
			wg.Add(1)
			go func() {
				defer wg.Done()
				registerDestination("worker-2", "kind-"+Worker2Cluster, platformCtx, platformIP)
			}()
		}
		wg.Wait()
	}
}

func registerDestination(name, targetCtx, platformCtx, platformIP string) {
	fmt.Printf("🔗 Registering %s\n", name)

	// A. Register Destination on Platform Cluster
	// Instead of a sample file, we use the specific register-destination flags logic
	storeKind := "BucketStateStore"
	if UseGit {
		storeKind = "GitStateStore"
	}

	destManifest := fmt.Sprintf(`---
apiVersion: platform.kratix.io/v1alpha1
kind: Destination
metadata:
  name: %s
spec:
  stateStoreRef:
    name: default
    kind: %s
  path: %s
  strictMatchLabels: false
`, name, storeKind, name)

	retry(5, 5*time.Second, func() error { return pipeToKubectl(destManifest, platformCtx) })
	if name == "worker-1" {
		sh.RunV("kubectl", "--context", platformCtx, "label", "destination", name, "environment=dev", "--overwrite")
	}

	// B. Install Flux on Worker
	sh.RunV("kubectl", "--context", targetCtx, "apply", "-f", filepath.Join(Root, "hack/destination/gitops-tk-install.yaml"))

	resourceFile := filepath.Join(Root, "hack/destination/gitops-tk-resources.yaml")
	if UseGit {
		resourceFile = filepath.Join(Root, "hack/destination/gitops-tk-resources-git.yaml")
		sh.RunV("kubectl", "create", "ns", "flux-system", "--context", targetCtx)
		sh.Run("sh", "-c", fmt.Sprintf("kubectl get secret gitea-credentials --context %s -n gitea -o yaml | grep -v 'namespace:' | kubectl apply --ns flux-system --context %s -f -", platformCtx, targetCtx))
	}

	content, err := os.ReadFile(resourceFile)
	if err != nil {
		fmt.Printf("❌ Failed to read GitOps resources: %v\n", err)
		return
	}
	patched := strings.ReplaceAll(string(content), "172.18.0.2", platformIP)
	patched = strings.ReplaceAll(patched, "worker-1", name)
	pipeToKubectl(patched, targetCtx)
}

// --- Sync & Wait ---

func WaitForSystem() error {
	platformCtx := "kind-" + PlatformCluster
	targetCtx := "kind-" + Worker1Cluster
	if SingleCluster {
		targetCtx = platformCtx
	}

	for i := 0; i < 40; i++ {
		if err := exec.Command("kubectl", "--context", targetCtx, "get", "ns", "kratix-worker-system").Run(); err == nil {
			return nil
		}
		now := time.Now().Unix()
		exec.Command("kubectl", "--context", targetCtx, "annotate", "--field-manager=flux-client-side-apply", "--overwrite", "-n", "flux-system", "bucket/kratix", fmt.Sprintf("reconcile.fluxcd.io/requestedAt=%d", now)).Run()
		exec.Command("kubectl", "--context", targetCtx, "annotate", "--field-manager=flux-client-side-apply", "--overwrite", "-n", "flux-system", "gitrepository/kratix-source", fmt.Sprintf("reconcile.fluxcd.io/requestedAt=%d", now)).Run()
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout waiting for kratix-worker-system")
}

// --- Helpers ---

func pipeToKubectl(manifest, ctx string) error {
	if strings.TrimSpace(manifest) == "" {
		return fmt.Errorf("manifest is empty")
	}
	cmd := exec.Command("kubectl", "--context", ctx, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cleanupJobs() {
	ctx := "kind-" + PlatformCluster
	sh.Run("kubectl", "delete", "job", "minio-create-bucket", "-n", "default", "--context", ctx)
	sh.Run("kubectl", "delete", "job", "gitea-create-repository", "-n", "gitea", "--context", ctx)
}

func retry(attempts int, sleep time.Duration, f func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil {
			return nil
		}
		fmt.Printf("⚠️  Retry %d/%d...\n", i+1, attempts)
		time.Sleep(sleep)
	}
	return err
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	val := strings.ToLower(os.Getenv(key))
	return val == "true" || val == "1"
}
