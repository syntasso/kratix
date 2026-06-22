package writers

import "github.com/go-logr/logr"

// NewGitWriterForTest constructs a GitWriter against a supplied executor.
// It exists so tests can exercise the writer in isolation without going
// through NewGitWriter (which builds a real git client).
func NewGitWriterForTest(runner GitExecutor, url, branch, authorName, authorEmail, repoPath string, log logr.Logger) *GitWriter {
	return &GitWriter{
		GitServer: gitServer{URL: url, Branch: branch},
		Author:    gitAuthor{Name: authorName, Email: authorEmail},
		Path:      repoPath,
		Log:       log,
		Runner:    runner,
	}
}

// URL exposes the writer's configured remote URL for assertions in tests.
func (g *GitWriter) URL() string { return g.GitServer.URL }

// Branch exposes the writer's configured branch for assertions in tests.
func (g *GitWriter) Branch() string { return g.GitServer.Branch }

// AuthorName exposes the writer's configured author name for assertions in tests.
func (g *GitWriter) AuthorName() string { return g.Author.Name }

// AuthorEmail exposes the writer's configured author email for assertions in tests.
func (g *GitWriter) AuthorEmail() string { return g.Author.Email }
