package git //nolint:testpackage // These white-box tests build the client without a real credential setup.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Push", func() {
	var (
		remoteURL string
		client    *nativeGitClient
		commitSHA string
	)

	BeforeEach(func() {
		remoteURL, _ = setupRemoteRepo()
		client = &nativeGitClient{
			repoURL:              remoteURL,
			creds:                NopCreds{},
			log:                  logr.Discard(),
			minimumFetchInterval: DefaultMinimumFetchInterval,
		}
		_, err := client.Clone("main")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(client.Root()) })

		Expect(os.WriteFile(filepath.Join(client.Root(), "file.txt"), []byte("changed"), 0o644)).To(Succeed())
		_, err = client.Add(filepath.Join(client.Root(), "file.txt"))
		Expect(err).NotTo(HaveOccurred())
		commitSHA, err = client.Commit("change", "test", "test@example.com")
		Expect(err).NotTo(HaveOccurred())
	})

	It("pushes the commit to the branch that was cloned", func() {
		_, err := client.Push("main", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(remoteBranchSHA(remoteURL, "main")).To(Equal(commitSHA))
	})

	It("pushes the commit to a branch that only exists on the remote after the push", func() {
		_, err := client.Push("kratix/default/my-workplacement", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(remoteBranchSHA(remoteURL, "kratix/default/my-workplacement")).To(Equal(commitSHA))
		Expect(remoteBranchSHA(remoteURL, "main")).NotTo(Equal(commitSHA))
	})
})

func remoteBranchSHA(remoteURL, branch string) string {
	GinkgoHelper()
	out, err := exec.Command("git", "ls-remote", remoteURL, "refs/heads/"+branch).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.Fields(string(out) + " ")[0]
}
