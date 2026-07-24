package git //nolint:testpackage // These white-box tests exercise interrupted operations and fetch timing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResetToRemote", func() {
	var (
		remoteURL string
		seedDir   string
		client    *nativeGitClient
	)

	BeforeEach(func() {
		remoteURL, seedDir = setupRemoteRepo()
		client = &nativeGitClient{
			repoURL:              remoteURL,
			creds:                NopCreds{},
			log:                  logr.Discard(),
			minimumFetchInterval: DefaultMinimumFetchInterval,
		}
		_, err := client.Clone("main")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(client.Root())
	})

	When("a file was deleted directly on the remote", func() {
		It("resets the local clone to match the remote", func() {
			localFile := filepath.Join(client.Root(), "file.txt")
			Expect(localFile).To(BeAnExistingFile())

			gitInDir(seedDir, "rm", "file.txt")
			gitInDir(seedDir, "commit", "-m", "external delete")
			gitInDir(seedDir, "push", "origin", "main")

			// Force a fetch rather than waiting out the coalesce window.
			client.lastFetch = time.Time{}

			Expect(client.ResetToRemote("main")).To(Succeed())
			Expect(localFile).NotTo(BeAnExistingFile())
		})
	})

	When("a fetch happened within the coalesce window", func() {
		It("skips the fetch and does not pick up remote changes", func() {
			gitInDir(seedDir, "commit", "--allow-empty", "-m", "noop") // ensure remote moved
			Expect(os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("changed remotely"), 0o644)).To(Succeed())
			gitInDir(seedDir, "add", ".")
			gitInDir(seedDir, "commit", "-m", "remote change")
			gitInDir(seedDir, "push", "origin", "main")

			// Clone already stamped lastFetch to now, so we are inside the window.
			Expect(client.ResetToRemote("main")).To(Succeed())

			content, err := os.ReadFile(filepath.Join(client.Root(), "file.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("original"))
		})
	})

	When("the minimum fetch interval is zero", func() {
		It("fetches and picks up remote changes every time", func() {
			client.minimumFetchInterval = 0

			Expect(os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("changed remotely"), 0o644)).To(Succeed())
			gitInDir(seedDir, "add", ".")
			gitInDir(seedDir, "commit", "-m", "remote change")
			gitInDir(seedDir, "push", "origin", "main")

			Expect(client.ResetToRemote("main")).To(Succeed())

			content, err := os.ReadFile(filepath.Join(client.Root(), "file.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("changed remotely"))
		})
	})
})

// setupRemoteRepo creates a bare "remote" repo seeded with a single file on the
// main branch, plus a working "seed" clone used to make changes to that remote.
func setupRemoteRepo() (remoteURL string, seedDir string) {
	GinkgoHelper()
	base, err := os.MkdirTemp("", "git-reset-remote")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = os.RemoveAll(base) })

	bare := filepath.Join(base, "remote.git")
	gitInDir(base, "init", "--bare", bare)
	remoteURL = "file://" + bare

	seedDir = filepath.Join(base, "seed")
	Expect(os.Mkdir(seedDir, 0o755)).To(Succeed())
	gitInDir(seedDir, "init")
	gitInDir(seedDir, "symbolic-ref", "HEAD", "refs/heads/main")
	Expect(os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("original"), 0o644)).To(Succeed())
	gitInDir(seedDir, "add", ".")
	gitInDir(seedDir, "commit", "-m", "seed")
	gitInDir(seedDir, "remote", "add", "origin", remoteURL)
	gitInDir(seedDir, "push", "-u", "origin", "main")
	return remoteURL, seedDir
}

// gitInDir runs a git command in dir, isolated from the developer's own git
// config and hooks so the tests behave the same everywhere.
func gitInDir(dir string, args ...string) {
	GinkgoHelper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}
