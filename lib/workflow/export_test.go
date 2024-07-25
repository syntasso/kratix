package workflow

import "time"

func SetMinimumPeriodBetweenCreatingPipelineResources(t time.Duration) {
	minimumPeriodBetweenCreatingPipelineResources = t
}
