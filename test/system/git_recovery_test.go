package system_test

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

// These tests cover the writer recovering when the Git state store is changed
// outside of Kratix (a manual commit, a Flux/Argo prune, another tool). They
// mutate the in-KinD Gitea repository directly, so they only run against KinD
// (not the LRE, which uses a real GitHub repository).
var _ = Describe("GitStateStore writer recovery", Label("git-recovery"), Serial, func() {
	BeforeEach(func() {
		if os.Getenv("LRE") == "true" {
			Skip("external repo mutation targets the in-KinD Gitea; not applicable on the LRE")
		}
		SetDefaultEventuallyTimeout(3 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(3*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", "assets/destination/destination-git-test-store.yaml")
		platform.Kubectl("apply", "-f", "assets/destination/destination-git-recovery.yaml")
		platform.Kubectl("apply", "-f", "assets/destination/promise-git-recovery.yaml")

		Eventually(func() string {
			return platform.Kubectl("get", "crds")
		}).Should(ContainSubstring("gitrecoveries.test.kratix.io"))

		platform.Kubectl("apply", "-f", "assets/destination/resource-git-recovery.yaml")
	})

	AfterEach(func() {
		platform.Kubectl("delete", "-f", "assets/destination/resource-git-recovery.yaml", "--ignore-not-found")
		platform.Kubectl("delete", "promises", "gitrecovery", "--ignore-not-found")
		platform.Kubectl("delete", "-f", "assets/destination/destination-git-recovery.yaml", "--ignore-not-found")
	})

	It("restores externally-deleted files and reports honest status", func() {
		By("writing the resource's files to the state store", func() {
			Eventually(func() string {
				return platform.Kubectl("get", "gitrecovery", "example")
			}).Should(ContainSubstring("Reconciled"))

			Eventually(gitRecoveryResourceFiles).Should(ContainElement("gitrecovery-configmap.yaml"))
			Eventually(gitRecoveryConfigMapContent).Should(ContainSubstring("value: one"))
		})

		By("deleting the resource's directory directly on the remote", func() {
			deleteGitRecoveryResourcesFromRemote()
			Expect(gitRecoveryResourceFiles()).NotTo(ContainElement("gitrecovery-configmap.yaml"))
		})

		By("changing the resource so it is written again", func() {
			platform.Kubectl("patch", "gitrecovery", "example", "--type=merge", "-p", `{"spec":{"value":"two"}}`)
		})

		By("recovering: the file reappears on the remote with the new content", func() {
			Eventually(gitRecoveryResourceFiles).Should(ContainElement("gitrecovery-configmap.yaml"))
			Eventually(gitRecoveryConfigMapContent).Should(ContainSubstring("value: two"))
		})

		By("reporting the WorkPlacement as Ready once the remote actually has the file", func() {
			Expect(gitRecoveryResourceFiles()).To(ContainElement("gitrecovery-configmap.yaml"))
			Eventually(func() string {
				return platform.Kubectl("-n", "default", "get", "workplacements",
					"-l", "kratix.io/targetDestinationName=git-recovery")
			}).Should(ContainSubstring("Ready"))
		})
	})
})

// git shells out to the git CLI, mirroring how `mc` shells out to the MinIO
// client in the destination tests. dir is the working directory (empty for the
// current directory), and TLS verification is skipped because Gitea uses a
// self-signed certificate.
func git(dir string, args ...string) string {
	GinkgoHelper()
	command := exec.Command("git", args...) //nolint:gosec
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_SSL_NO_VERIFY=true", "GIT_TERMINAL_PROMPT=0")
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(session).Should(gexec.Exit(0))
	return string(session.Out.Contents())
}

// giteaRepoURL returns the host-reachable URL of the Gitea repository backing
// the state store. The controller reaches Gitea via its in-cluster service; from
// the test process it is reached on localhost via the NodePort that KinD maps to
// the host (the same way `mc` reaches MinIO). TLS verification is skipped
// elsewhere because Gitea uses a self-signed certificate.
func giteaRepoURL() string {
	GinkgoHelper()
	encodedPassword := platform.Kubectl("get", "secret", "gitea-credentials", "-n", "default", "-o", "jsonpath={.data.password}")
	password, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedPassword))
	Expect(err).NotTo(HaveOccurred())

	return fmt.Sprintf("https://gitea_admin:%s@localhost:31333/gitea_admin/kratix.git", string(password))
}

// cloneStateStore clones a fresh copy of the state store repository into a
// temporary directory and returns its path.
func cloneStateStore() string {
	GinkgoHelper()
	dir, err := os.MkdirTemp("", "kratix-system-test-repo")
	Expect(err).NotTo(HaveOccurred())
	git("", "clone", "--quiet", giteaRepoURL(), dir)
	return dir
}

// gitRecoveryResourceFiles returns the base names of the files the git-recovery
// resource has written to the state store (under its destination path).
func gitRecoveryResourceFiles() []string {
	GinkgoHelper()
	dir := cloneStateStore()
	defer os.RemoveAll(dir)

	var files []string
	root := filepath.Join(dir, "git-recovery", "resources")
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, filepath.Base(path))
		}
		return nil
	})
	return files
}

// gitRecoveryConfigMapContent returns the contents of the ConfigMap written by
// the git-recovery resource, or an empty string if it is not present.
func gitRecoveryConfigMapContent() string {
	GinkgoHelper()
	dir := cloneStateStore()
	defer os.RemoveAll(dir)

	var content string
	root := filepath.Join(dir, "git-recovery", "resources")
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Base(path) == "gitrecovery-configmap.yaml" {
			bytes, readErr := os.ReadFile(path) //nolint:gosec
			if readErr == nil {
				content = string(bytes)
			}
		}
		return nil
	})
	return content
}

// deleteGitRecoveryResourcesFromRemote deletes the resource's directory directly
// on the remote branch, simulating a manual prune outside of Kratix.
func deleteGitRecoveryResourcesFromRemote() {
	GinkgoHelper()
	dir := cloneStateStore()
	defer os.RemoveAll(dir)

	git(dir, "rm", "-r", "--quiet", "git-recovery/resources")
	// --no-verify skips any local commit hooks; this is a throwaway clone.
	git(dir, "-c", "user.name=external", "-c", "user.email=external@example.com",
		"commit", "--no-verify", "--quiet", "-m", "external delete of git-recovery resources")
	git(dir, "push", "--no-verify", "--quiet", "origin", "main")
}
