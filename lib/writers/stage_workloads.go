package writers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

var ErrPathOutsideRepository = fmt.Errorf("workload path is outside the git repository")

// StageWorkloads writes each workload under dir in the clone and stages it.
func StageWorkloads(runner GitExecutor, dir string, workloads []v1alpha1.Workload, logger logr.Logger) error {
	localDir := runner.Root()

	filesToAdd := make([]string, 0, len(workloads))
	for _, file := range workloads {
		log := logger.WithValues("filepath", file.Filepath)

		absoluteFilePath := filepath.Join(dir, file.Filepath)

		// filepath.Join resolves any `..`, so a path is only accepted when the
		// result still sits inside the clone.
		if !strings.HasPrefix(absoluteFilePath, localDir) {
			logging.Warn(log,
				"path of file to write is not located within the git repository",
				"absolutePath", absoluteFilePath, "tmpDir", localDir)
			return ErrPathOutsideRepository
		}

		if err := os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
			logging.Error(log, err, "could not generate local directories")
			return err
		}

		if err := os.WriteFile(absoluteFilePath, []byte(file.Content), 0644); err != nil {
			logging.Error(log, err, "could not write to file")
			return err
		}
		filesToAdd = append(filesToAdd, absoluteFilePath)
	}

	if len(filesToAdd) == 0 {
		return nil
	}

	if _, err := runner.Add(filesToAdd...); err != nil {
		logging.Error(logger, err, "could not add files to worktree")
		return err
	}
	return nil
}
