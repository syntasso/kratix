package workflow

import (
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
)

func SetMinimumPeriodBetweenCreatingPipelineResources(t time.Duration) {
	minimumPeriodBetweenCreatingPipelineResources = t
}

// MigrateWorkflowStatus exposes the engine-internal status migration. The engine
// returns from ReconcileConfigure the moment the migration changes anything, so
// its idempotence is only assertable by calling it directly.
func MigrateWorkflowStatus(opts Opts, key string, action v1alpha1.Action) (bool, error) {
	return migrateWorkflowStatus(opts, key, action)
}
