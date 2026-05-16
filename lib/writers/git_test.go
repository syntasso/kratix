package writers_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	"github.com/syntasso/kratix/util/git"
)

// fakeBatchGitExecutor satisfies both writers.GitExecutor and
// writers.BatchFileRemover so we can exercise the batch-removal branch in
// GitWriter.deleteExistingFiles.
type fakeBatchGitExecutor struct {
	*writersfakes.FakeGitExecutor
	*writersfakes.FakeBatchFileRemover
}

func (f *fakeBatchGitExecutor) Root() string {
	return f.FakeGitExecutor.Root()
}

var _ = Describe("GitWriter", func() {
	var (
		logger logr.Logger
		dest   v1alpha1.Destination
		spec   v1alpha1.GitStateStoreSpec
	)

	BeforeEach(func() {
		logger = ctrl.Log.WithName("test")
		spec = v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path: "state-store-path",
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "dummy-secret",
				},
			},
			AuthMethod: v1alpha1.BasicAuthMethod,
			URL:        "https://github.com/syntasso/kratix",
			Branch:     "test",
			GitAuthor: v1alpha1.GitAuthor{
				Email: "test@example.com",
				Name:  "a-user",
			},
		}
		dest = v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test"},
			Spec:       v1alpha1.DestinationSpec{Path: "dst-path/"},
		}
	})

	Describe("NewGitWriter", func() {
		It("constructs a GitWriter with the provided state store spec", func() {
			creds := map[string][]byte{
				"username": []byte("user1"),
				"password": []byte("pw1"),
			}

			writer, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, creds)
			Expect(err).NotTo(HaveOccurred())

			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.URL()).To(Equal(spec.URL))
			Expect(gitWriter.Branch()).To(Equal(spec.Branch))
			Expect(gitWriter.AuthorEmail()).To(Equal(spec.GitAuthor.Email))
			Expect(gitWriter.AuthorName()).To(Equal(spec.GitAuthor.Name))
			Expect(gitWriter.Path).To(Equal("state-store-path/dst-path"))
		})

		It("strips leading slashes from the StateStore and Destination paths", func() {
			creds := map[string][]byte{
				"username": []byte("user1"),
				"password": []byte("pw1"),
			}

			By("removing the leading slash from StateStore.Path", func() {
				spec.Path = "/test"
				writer, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, creds)
				Expect(err).NotTo(HaveOccurred())
				Expect(writer.(*writers.GitWriter).Path).To(HavePrefix("test"))
			})

			By("removing the leading slash from Destination.Path", func() {
				spec.Path = ""
				dest.Spec.Path = "/dst-test"
				writer, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, creds)
				Expect(err).NotTo(HaveOccurred())
				Expect(writer.(*writers.GitWriter).Path).To(HavePrefix("dst-test"))
			})
		})

		It("constructs a GitWriter when authenticating with SSH", func() {
			spec.AuthMethod = v1alpha1.SSHAuthMethod
			spec.URL = "test-user@test.ghe.com:test-org/test-state-store.git"

			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).NotTo(HaveOccurred())

			writer, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, sshCreds(key))
			Expect(err).NotTo(HaveOccurred())

			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.URL()).To(Equal(spec.URL))
			Expect(gitWriter.Branch()).To(Equal(spec.Branch))
			Expect(gitWriter.AuthorEmail()).To(Equal(spec.GitAuthor.Email))
			Expect(gitWriter.AuthorName()).To(Equal(spec.GitAuthor.Name))
		})

		Context("when authenticating with GitHub App", func() {
			var (
				origJWT     func(string, string) (string, error)
				origToken   func(string, string, string) (string, error)
				jwtCalled   bool
				tokenCalled bool
			)

			BeforeEach(func() {
				jwtCalled = false
				tokenCalled = false
				origJWT = git.GenerateGitHubAppJWT
				origToken = git.GetGitHubInstallationToken
				git.GenerateGitHubAppJWT = func(string, string) (string, error) {
					jwtCalled = true
					return "jwt", nil
				}
				git.GetGitHubInstallationToken = func(string, string, string) (string, error) {
					tokenCalled = true
					return "token", nil
				}
			})

			AfterEach(func() {
				git.GenerateGitHubAppJWT = origJWT
				git.GetGitHubInstallationToken = origToken
			})

			It("routes through GenerateGitHubAppJWT and GetGitHubInstallationToken", func() {
				spec.AuthMethod = v1alpha1.GitHubAppAuthMethod
				writer, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, map[string][]byte{
					"appID":          []byte("123"),
					"installationID": []byte("456"),
					"privateKey":     []byte("dummy"),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				Expect(jwtCalled).To(BeTrue(), "GenerateGitHubAppJWT should be invoked")
				Expect(tokenCalled).To(BeTrue(), "GetGitHubInstallationToken should be invoked")
			})
		})

		It("returns an error when SetAuth fails", func() {
			creds := map[string][]byte{"username": []byte("user1")} // missing password
			_, err := writers.NewGitWriter(logger, spec, dest.Spec.Path, creds)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("could not create auth method"))
		})
	})

	Describe("operations using a mocked GitExecutor", func() {
		var (
			fakeRunner *writersfakes.FakeGitExecutor
			tmpRoot    string
			gitWriter  *writers.GitWriter
			repoPath   string
		)

		BeforeEach(func() {
			var err error
			tmpRoot, err = os.MkdirTemp("", "kratix-git-writer-test-")
			Expect(err).NotTo(HaveOccurred())

			repoPath = "state-store-path/dst-path"
			fakeRunner = &writersfakes.FakeGitExecutor{}
			fakeRunner.RootReturns(tmpRoot)

			gitWriter = writers.NewGitWriterForTest(
				fakeRunner,
				spec.URL,
				spec.Branch,
				spec.GitAuthor.Name,
				spec.GitAuthor.Email,
				repoPath,
				logger,
			)
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmpRoot)).To(Succeed())
		})

		Describe("Init", func() {
			It("delegates to Runner.Clone", func() {
				fakeRunner.CloneReturns("/tmp/clone-root", nil)

				root, err := gitWriter.Init("main")
				Expect(err).NotTo(HaveOccurred())
				Expect(root).To(Equal("/tmp/clone-root"))
				Expect(fakeRunner.CloneCallCount()).To(Equal(1))
				Expect(fakeRunner.CloneArgsForCall(0)).To(Equal("main"))
			})

			It("returns the underlying error from Clone", func() {
				fakeRunner.CloneReturns("", errors.New("boom"))

				_, err := gitWriter.Init("main")
				Expect(err).To(MatchError("boom"))
			})
		})

		Describe("Reset", func() {
			It("checks out the configured branch", func() {
				Expect(gitWriter.Reset()).To(Succeed())
				Expect(fakeRunner.CheckoutCallCount()).To(Equal(1))
				Expect(fakeRunner.CheckoutArgsForCall(0)).To(Equal(spec.Branch))
			})

			It("returns the underlying error from Checkout", func() {
				fakeRunner.CheckoutReturns("", errors.New("checkout failed"))
				Expect(gitWriter.Reset()).To(MatchError("checkout failed"))
			})
		})

		Describe("ValidatePermissions", func() {
			It("does a dry-run push to the configured branch", func() {
				Expect(gitWriter.ValidatePermissions()).To(Succeed())
				Expect(fakeRunner.PushCallCount()).To(Equal(1))
				branch, force := fakeRunner.PushArgsForCall(0)
				Expect(branch).To(Equal(spec.Branch))
				Expect(force).To(BeFalse())
			})

			It("wraps the push error", func() {
				fakeRunner.PushReturns("", errors.New("denied"))
				err := gitWriter.ValidatePermissions()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("write permission validation failed"))
				Expect(err.Error()).To(ContainSubstring("denied"))
			})
		})

		Describe("ReadFile", func() {
			It("returns ErrFileNotFound when the file is missing", func() {
				_, err := gitWriter.ReadFile("missing.yaml")
				Expect(err).To(MatchError(writers.ErrFileNotFound))
			})

			It("returns the file content from the worktree", func() {
				absDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(absDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(absDir, "foo.yaml"), []byte("hello"), 0644)).To(Succeed())

				content, err := gitWriter.ReadFile("foo.yaml")
				Expect(err).NotTo(HaveOccurred())
				Expect(content).To(Equal([]byte("hello")))
			})
		})

		Describe("UpdateFiles", func() {
			It("is a no-op when there are no workloads and no subDir", func() {
				sha, err := gitWriter.UpdateFiles("", "wp", nil, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(sha).To(BeEmpty())
				Expect(fakeRunner.AddCallCount()).To(BeZero())
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("writes new files, adds them, and commits", func() {
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("abc123", nil)

				workloads := []v1alpha1.Workload{
					{Filepath: "a.yaml", Content: "a: 1"},
					{Filepath: "nested/b.yaml", Content: "b: 2"},
				}

				sha, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(sha).To(Equal("abc123"))

				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")

				// File contents match the workloads byte-for-byte.
				aContent, err := os.ReadFile(filepath.Join(baseDir, "a.yaml"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(aContent)).To(Equal("a: 1"))
				bContent, err := os.ReadFile(filepath.Join(baseDir, "nested/b.yaml"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(bContent)).To(Equal("b: 2"))

				Expect(fakeRunner.AddCallCount()).To(Equal(1))
				added := fakeRunner.AddArgsForCall(0)
				Expect(added).To(ConsistOf(
					filepath.Join(baseDir, "a.yaml"),
					filepath.Join(baseDir, "nested/b.yaml"),
				))
				// Add must receive absolute paths inside the runner's root.
				for _, p := range added {
					Expect(filepath.IsAbs(p)).To(BeTrue(), "Add path %q should be absolute", p)
					Expect(p).To(HavePrefix(tmpRoot), "Add path %q should be inside the worktree", p)
				}

				Expect(fakeRunner.CommitAndPushCallCount()).To(Equal(1))
				branch, msg, name, email := fakeRunner.CommitAndPushArgsForCall(0)
				Expect(branch).To(Equal(spec.Branch))
				Expect(msg).To(Equal("Update from: wp"))
				Expect(name).To(Equal(spec.GitAuthor.Name))
				Expect(email).To(Equal(spec.GitAuthor.Email))
			})

			It("skips files that would escape the git root and aborts the whole batch", func() {
				// Compute a path with enough "../" segments to climb above
				// tmpRoot, regardless of where tmpRoot lives on disk.
				depth := strings.Count(tmpRoot, string(os.PathSeparator)) + 4
				escape := strings.Repeat("../", depth) + "etc/passwd"

				// Include a benign file before the escaping one. The writer
				// bails out the moment it sees the bad path, so the earlier
				// file ends up on disk but is never staged/committed.
				workloads := []v1alpha1.Workload{
					{Filepath: "ok.yaml", Content: "ok"},
					{Filepath: escape, Content: "nope"},
				}
				sha, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(sha).To(BeEmpty())
				Expect(fakeRunner.AddCallCount()).To(BeZero())
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())

				// The earlier file leaked to disk (current behaviour); the
				// escaping file did not. Lock this behaviour in.
				okPath := filepath.Join(tmpRoot, gitWriter.Path, "subdir", "ok.yaml")
				Expect(okPath).To(BeAnExistingFile())
				Expect(filepath.Join(tmpRoot, "etc", "passwd")).NotTo(BeAnExistingFile())
			})

			It("returns the error when Add fails", func() {
				fakeRunner.AddReturns("", errors.New("add failed"))
				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				_, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).To(MatchError("add failed"))
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("returns the error when HasChanges fails", func() {
				fakeRunner.HasChangesReturns(false, errors.New("status failed"))
				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				_, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).To(MatchError("status failed"))
			})

			It("returns the error when CommitAndPush fails", func() {
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("", errors.New("push failed"))
				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				_, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).To(MatchError("push failed"))
			})

			It("does not commit when there are no local changes", func() {
				fakeRunner.HasChangesReturns(false, nil)
				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				sha, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(sha).To(BeEmpty())
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("removes the entire subdirectory when subDir is provided with no workloads to create", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("delete-sha", nil)

				sha, err := gitWriter.UpdateFiles("subdir", "wp", nil, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(sha).To(Equal("delete-sha"))

				Expect(fakeRunner.RemoveDirectoryCallCount()).To(Equal(1))
				Expect(fakeRunner.RemoveDirectoryArgsForCall(0)).To(Equal(baseDir))
				_, msg, _, _ := fakeRunner.CommitAndPushArgsForCall(0)
				Expect(msg).To(Equal("Delete from: wp"))
			})

			It("propagates CommitAndPush errors on the subdirectory-purge path", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("", errors.New("push failed"))

				_, err := gitWriter.UpdateFiles("subdir", "wp", nil, nil)
				Expect(err).To(MatchError("push failed"))
				Expect(fakeRunner.RemoveDirectoryCallCount()).To(Equal(1))
			})

			It("propagates HasChanges errors on the subdirectory-purge path", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				fakeRunner.HasChangesReturns(false, errors.New("status failed"))

				_, err := gitWriter.UpdateFiles("subdir", "wp", nil, nil)
				Expect(err).To(MatchError("status failed"))
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("returns the error when RemoveDirectory fails", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				fakeRunner.RemoveDirectoryReturns(errors.New("rmdir failed"))

				_, err := gitWriter.UpdateFiles("subdir", "wp", nil, nil)
				Expect(err).To(MatchError("rmdir failed"))
			})

			It("does not call RemoveDirectory when the subdir does not exist on disk", func() {
				// No mkdir for baseDir: simulate first-time write into subdir.
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("sha", nil)

				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				_, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeRunner.RemoveDirectoryCallCount()).To(BeZero())
				_, msg, _, _ := fakeRunner.CommitAndPushArgsForCall(0)
				Expect(msg).To(Equal("Update from: wp"))
			})

			It("uses the 'Update' action even when subdir is provided alongside workloads to create", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path, "subdir")
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("sha", nil)

				workloads := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a"}}
				_, err := gitWriter.UpdateFiles("subdir", "wp", workloads, nil)
				Expect(err).NotTo(HaveOccurred())

				// subdir existed -> RemoveDirectory IS called before the new files are written.
				Expect(fakeRunner.RemoveDirectoryCallCount()).To(Equal(1))
				_, msg, _, _ := fakeRunner.CommitAndPushArgsForCall(0)
				Expect(msg).To(Equal("Update from: wp"))
			})

			It("does not invoke RemoveDirectory when only workloadsToDelete is set (no subDir)", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "f.yaml"), []byte("x"), 0644)).To(Succeed())
				fakeRunner.HasChangesReturns(true, nil)

				_, err := gitWriter.UpdateFiles("", "wp", nil, []string{"f.yaml"})
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeRunner.RemoveDirectoryCallCount()).To(BeZero())
				_, msg, _, _ := fakeRunner.CommitAndPushArgsForCall(0)
				Expect(msg).To(Equal("Delete from: wp"))
			})

			It("removes only listed files one by one when the runner does not implement BatchFileRemover", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				existing := filepath.Join(baseDir, "exists.yaml")
				Expect(os.WriteFile(existing, []byte("x"), 0644)).To(Succeed())

				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("sha", nil)

				_, err := gitWriter.UpdateFiles("", "wp", nil, []string{"exists.yaml", "missing.yaml"})
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeRunner.RemoveFileCallCount()).To(Equal(1))
				Expect(fakeRunner.RemoveFileArgsForCall(0)).To(Equal(existing))
			})

			It("uses RemoveFiles batching when the runner implements BatchFileRemover", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				one := filepath.Join(baseDir, "one.yaml")
				two := filepath.Join(baseDir, "two.yaml")
				Expect(os.WriteFile(one, []byte("1"), 0644)).To(Succeed())
				Expect(os.WriteFile(two, []byte("2"), 0644)).To(Succeed())

				batchFake := &writersfakes.FakeBatchFileRemover{}
				gitWriter = writers.NewGitWriterForTest(
					&fakeBatchGitExecutor{
						FakeGitExecutor:      fakeRunner,
						FakeBatchFileRemover: batchFake,
					},
					spec.URL, spec.Branch, spec.GitAuthor.Name, spec.GitAuthor.Email, repoPath, logger,
				)

				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("sha", nil)

				_, err := gitWriter.UpdateFiles("", "wp", nil, []string{"one.yaml", "two.yaml"})
				Expect(err).NotTo(HaveOccurred())

				Expect(batchFake.RemoveFilesCallCount()).To(Equal(1))
				Expect(batchFake.RemoveFilesArgsForCall(0)).To(ConsistOf(one, two))
				Expect(fakeRunner.RemoveFileCallCount()).To(BeZero())
			})

			It("returns the error when batched RemoveFiles fails", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "one.yaml"), []byte("1"), 0644)).To(Succeed())

				batchFake := &writersfakes.FakeBatchFileRemover{}
				batchFake.RemoveFilesReturns(errors.New("batch fail"))
				gitWriter = writers.NewGitWriterForTest(
					&fakeBatchGitExecutor{
						FakeGitExecutor:      fakeRunner,
						FakeBatchFileRemover: batchFake,
					},
					spec.URL, spec.Branch, spec.GitAuthor.Name, spec.GitAuthor.Email, repoPath, logger,
				)

				_, err := gitWriter.UpdateFiles("", "wp", nil, []string{"one.yaml"})
				Expect(err).To(MatchError("batch fail"))
			})

			It("returns the error when per-file RemoveFile fails", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "one.yaml"), []byte("1"), 0644)).To(Succeed())

				fakeRunner.RemoveFileReturns(errors.New("rm failed"))
				_, err := gitWriter.UpdateFiles("", "wp", nil, []string{"one.yaml"})
				Expect(err).To(MatchError("rm failed"))
			})
		})

		Describe("DeleteFiles", func() {
			It("removes the files that exist and commits", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				file := filepath.Join(baseDir, "to-delete.yaml")
				Expect(os.WriteFile(file, []byte("x"), 0644)).To(Succeed())
				dir := filepath.Join(baseDir, "to-delete-dir")
				Expect(os.MkdirAll(dir, 0755)).To(Succeed())

				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("sha", nil)

				Expect(gitWriter.DeleteFiles("wp", []string{
					"to-delete.yaml",
					"to-delete-dir",
					"missing.yaml",
				})).To(Succeed())

				Expect(fakeRunner.RemoveFileCallCount()).To(Equal(1))
				Expect(fakeRunner.RemoveFileArgsForCall(0)).To(Equal(file))
				Expect(fakeRunner.RemoveDirectoryCallCount()).To(Equal(1))
				Expect(fakeRunner.RemoveDirectoryArgsForCall(0)).To(Equal(dir))
				Expect(fakeRunner.CommitAndPushCallCount()).To(Equal(1))
			})

			It("propagates RemoveDirectory errors", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(filepath.Join(baseDir, "d"), 0755)).To(Succeed())
				fakeRunner.RemoveDirectoryReturns(errors.New("rmdir failed"))

				Expect(gitWriter.DeleteFiles("wp", []string{"d"})).
					To(MatchError("rmdir failed"))
			})

			It("propagates RemoveFile errors", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "f.yaml"), []byte("x"), 0644)).To(Succeed())
				fakeRunner.RemoveFileReturns(errors.New("rm failed"))

				Expect(gitWriter.DeleteFiles("wp", []string{"f.yaml"})).
					To(MatchError("rm failed"))
			})

			It("skips files that don't exist on disk and short-circuits when there are no changes", func() {
				// Nothing exists under the worktree, so neither RemoveFile nor
				// RemoveDirectory should be called. commitAndPush is still
				// invoked, but HasChanges=false makes it a no-op.
				fakeRunner.HasChangesReturns(false, nil)

				Expect(gitWriter.DeleteFiles("wp", []string{"nope.yaml", "also-missing/"})).To(Succeed())

				Expect(fakeRunner.RemoveFileCallCount()).To(BeZero())
				Expect(fakeRunner.RemoveDirectoryCallCount()).To(BeZero())
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("is a no-op when given an empty file list", func() {
				fakeRunner.HasChangesReturns(false, nil)

				Expect(gitWriter.DeleteFiles("wp", nil)).To(Succeed())
				Expect(gitWriter.DeleteFiles("wp", []string{})).To(Succeed())

				Expect(fakeRunner.RemoveFileCallCount()).To(BeZero())
				Expect(fakeRunner.RemoveDirectoryCallCount()).To(BeZero())
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("propagates HasChanges errors raised during the commit step", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "f.yaml"), []byte("x"), 0644)).To(Succeed())
				fakeRunner.HasChangesReturns(false, errors.New("status failed"))

				Expect(gitWriter.DeleteFiles("wp", []string{"f.yaml"})).
					To(MatchError("status failed"))
				Expect(fakeRunner.RemoveFileCallCount()).To(Equal(1))
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})

			It("propagates CommitAndPush errors raised during the commit step", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "f.yaml"), []byte("x"), 0644)).To(Succeed())
				fakeRunner.HasChangesReturns(true, nil)
				fakeRunner.CommitAndPushReturns("", errors.New("push failed"))

				Expect(gitWriter.DeleteFiles("wp", []string{"f.yaml"})).
					To(MatchError("push failed"))
				Expect(fakeRunner.RemoveFileCallCount()).To(Equal(1))
			})

			It("stops at the first error and does not process remaining files", func() {
				baseDir := filepath.Join(tmpRoot, gitWriter.Path)
				Expect(os.MkdirAll(baseDir, 0755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "first.yaml"), []byte("1"), 0644)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(baseDir, "second.yaml"), []byte("2"), 0644)).To(Succeed())

				fakeRunner.RemoveFileReturns(errors.New("rm failed"))

				Expect(gitWriter.DeleteFiles("wp", []string{"first.yaml", "second.yaml"})).
					To(MatchError("rm failed"))
				Expect(fakeRunner.RemoveFileCallCount()).To(Equal(1))
				Expect(fakeRunner.CommitAndPushCallCount()).To(BeZero())
			})
		})
	})
})

func sshCreds(key *rsa.PrivateKey) map[string][]byte {
	privateKeyPEM := pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	var b bytes.Buffer
	if err := pem.Encode(&b, &privateKeyPEM); err != nil {
		log.Fatalf("failed to write private key to buffer: %v", err)
	}
	return map[string][]byte{
		"sshPrivateKey": b.Bytes(),
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}
