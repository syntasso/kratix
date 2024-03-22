package controllers

import "github.com/syntasso/kratix/lib/workflow"

func SetReconcileConfigurePipeline(f func(workflow.Opts) (bool, error)) {
	reconcileConfigure = f
}

func SetReconcileDeletePipeline(f func(workflow.Opts, workflow.Pipeline) (bool, error)) {
	reconcileDelete = f
}
