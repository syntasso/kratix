package git_writer_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git Suite")
}

// testBranch is the branch every spec pushes to. Each run gets its own branch
// on the shared fixture repo so concurrent CI runs cannot race each other's
// pushes to the same ref.
var testBranch string

const fixtureRepoAPI = "https://api.github.com/repos/syntasso/testing-git-writer-private"

// staleBranchAge is how old a leftover ci-* branch must be before the janitor
// deletes it. Branch age comes from the timestamp encoded in the branch name,
// not the tip commit date: a fresh branch points at main's tip, whose commit
// may already be days old.
const staleBranchAge = 24 * time.Hour

var _ = BeforeSuite(func() {
	deleteStaleRunBranches()
	testBranch = runBranchName()
	fixtureAPICreateBranch(testBranch, fixtureAPIGetMainSHA())
})

var _ = AfterSuite(func() {
	// Best effort: if the process dies before this runs, the janitor in the
	// next run's BeforeSuite removes the leftover branch.
	fixtureAPIDeleteBranch(testBranch)
})

func runBranchName() string {
	suffix := fmt.Sprintf("local-%d", rand.Int())
	if runID := os.Getenv("GITHUB_RUN_ID"); runID != "" {
		suffix = fmt.Sprintf("%s-%s", runID, os.Getenv("GITHUB_RUN_ATTEMPT"))
	}
	return fmt.Sprintf("ci-%d-%s", time.Now().Unix(), suffix)
}

func deleteStaleRunBranches() {
	// The janitor is best effort: any failure here just leaves cleanup to a
	// future run rather than failing this one.
	status, body := fixtureAPIDo(http.MethodGet, "/git/matching-refs/heads/ci-", nil)
	if status != http.StatusOK {
		return
	}
	var refs []struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &refs) != nil {
		return
	}
	for _, ref := range refs {
		name := strings.TrimPrefix(ref.Ref, "refs/heads/")
		parts := strings.SplitN(name, "-", 3)
		if len(parts) < 3 {
			continue
		}
		createdAt, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		if time.Since(time.Unix(createdAt, 0)) > staleBranchAge {
			fixtureAPIDo(http.MethodDelete, "/git/refs/heads/"+name, nil)
		}
	}
}

func fixtureAPIGetMainSHA() string {
	GinkgoHelper()

	status, body := fixtureAPIDo(http.MethodGet, "/git/ref/heads/main", nil)
	Expect(status).To(Equal(http.StatusOK), string(body))
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	Expect(json.Unmarshal(body, &ref)).To(Succeed())
	return ref.Object.SHA
}

func fixtureAPICreateBranch(name, sha string) {
	GinkgoHelper()

	payload := fmt.Sprintf(`{"ref":"refs/heads/%s","sha":"%s"}`, name, sha)
	status, body := fixtureAPIDo(http.MethodPost, "/git/refs", strings.NewReader(payload))
	Expect(status).To(Equal(http.StatusCreated), string(body))
}

func fixtureAPIDeleteBranch(name string) {
	GinkgoHelper()

	// 204 = deleted, 422 = already gone; both leave the repo in the state we want.
	status, body := fixtureAPIDo(http.MethodDelete, "/git/refs/heads/"+name, nil)
	Expect(status).To(
		BeElementOf(http.StatusNoContent, http.StatusUnprocessableEntity), string(body))
}

func fixtureAPIDo(method, path string, body io.Reader) (int, []byte) {
	GinkgoHelper()

	req, err := http.NewRequest(method, fixtureRepoAPI+path, body)
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+os.Getenv("TEST_GIT_WRITER_GITHUB_HTTP_PAT"))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())
	return resp.StatusCode, respBody
}
