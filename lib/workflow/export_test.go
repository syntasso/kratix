package workflow

import "time"

func SetMinimumPeriodBetweenCreatingPipelineResources(t time.Duration) {
	minimumPeriodBetweenCreatingPipelineResources = t
}

// MigrateWorkflowStatus exposes the engine-internal status migration to the
// suite. The engine returns from ReconcileConfigure the moment the migration
// changes anything, so what a second run does — the idempotence contract — can
// only be asserted by calling the migration directly.
func MigrateWorkflowStatus(opts Opts, key string) (bool, error) {
	return migrateWorkflowStatus(opts, key)
}
