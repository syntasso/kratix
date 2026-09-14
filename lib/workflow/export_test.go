package workflow

import (
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
)

func SetMinimumPeriodBetweenCreatingPipelineResources(t time.Duration) {
	minimumPeriodBetweenCreatingPipelineResources = t
}

// RunHash is the hash the workflow records for a pipeline it has run.
func RunHash(pipeline v1alpha1.PipelineJobResources) string {
	return runHash(pipeline)
}
