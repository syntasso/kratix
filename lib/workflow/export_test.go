package workflow

import "time"

func SetMinimumPeriodBetweenCratingPipelineResources(t time.Duration) {
	minimumPeriodBetweenCreatingPipelineResources = t
}
