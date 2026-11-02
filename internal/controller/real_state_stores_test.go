package controller_test

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newLocalGitRemote makes a bare repository with one seed commit on main and points url at it.
func newLocalGitRemote(url string) string {
	GinkgoHelper()
	base := GinkgoT().TempDir()
	bare := filepath.Join(base, "remote.git")
	gitIn(base, "init", "--bare", "--initial-branch=main", bare)

	seed := filepath.Join(base, "seed")
	Expect(os.Mkdir(seed, 0o755)).To(Succeed())
	gitIn(seed, "init", "--initial-branch=main")
	Expect(os.WriteFile(filepath.Join(seed, "seed.txt"), []byte("seed"), 0o644)).To(Succeed())
	gitIn(seed, "add", ".")
	gitIn(seed, "commit", "-m", "seed")
	gitIn(seed, "push", "file://"+bare, "main")

	// The writer only accepts https URLs, so git rewrites this one to the local repository.
	config := filepath.Join(base, "gitconfig")
	Expect(os.WriteFile(config, fmt.Appendf(nil, "[url \"file://%s\"]\n\tinsteadOf = %s\n", bare, url), 0o644)).To(Succeed())
	GinkgoT().Setenv("GIT_CONFIG_GLOBAL", config)
	return bare
}

func gitIn(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=seed", "GIT_AUTHOR_EMAIL=seed@example.com",
		"GIT_COMMITTER_NAME=seed", "GIT_COMMITTER_EMAIL=seed@example.com",
	)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

func gitOutput(bare string, args ...string) string {
	GinkgoHelper()
	return gitIn(bare, args...)
}

func gitFiles(bare string) []string {
	GinkgoHelper()
	return strings.Split(gitIn(bare, "ls-tree", "-r", "--name-only", "main"), "\n")
}

// fakeBucket is an S3 endpoint that keeps objects in memory and serves what the S3 writer asks for when it removes files.
type fakeBucket struct {
	mu       sync.Mutex
	objects  map[string][]byte
	endpoint string
}

func newFakeBucket() *fakeBucket {
	bucket := &fakeBucket{objects: map[string][]byte{}}
	server := httptest.NewServer(bucket)
	DeferCleanup(server.Close)
	bucket.endpoint = strings.TrimPrefix(server.URL, "http://")
	return bucket
}

func (b *fakeBucket) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.objects))
	for key := range b.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type listedObject struct {
	Key          string `xml:"Key"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
	LastModified string `xml:"LastModified"`
}

type listBucketResult struct {
	XMLName     xml.Name       `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name        string         `xml:"Name"`
	Prefix      string         `xml:"Prefix"`
	KeyCount    int            `xml:"KeyCount"`
	MaxKeys     int            `xml:"MaxKeys"`
	IsTruncated bool           `xml:"IsTruncated"`
	Contents    []listedObject `xml:"Contents"`
}

func (b *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	modified := time.Unix(0, 0).UTC()
	w.Header().Set("Content-Type", "application/xml")

	switch {
	case key == "" && r.URL.Query().Has("location"):
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case key == "" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case key == "" && r.Method == http.MethodGet:
		prefix := r.URL.Query().Get("prefix")
		result := listBucketResult{Name: bucket, Prefix: prefix, MaxKeys: 1000}
		for objectKey, content := range b.objects {
			if strings.HasPrefix(objectKey, prefix) {
				result.Contents = append(result.Contents, listedObject{
					Key: objectKey, ETag: `"etag"`, Size: len(content), LastModified: modified.Format(time.RFC3339),
				})
			}
		}
		result.KeyCount = len(result.Contents)
		Expect(xml.NewEncoder(w).Encode(result)).To(Succeed())
	case r.Method == http.MethodHead:
		content, found := b.objects[key]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"etag"`)
		w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}
