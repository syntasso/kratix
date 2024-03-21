package controllers

import "github.com/syntasso/kratix/lib/manager"

func SetReconcileConfigurePipeline(f func(manager.WorkflowOpts) (bool, error)) {
	reconcileConfigurePipeline = f
}

func SetReconcileDeletePipeline(f func(manager.WorkflowOpts, manager.Pipeline) (bool, error)) {
	reconcileDeletePipeline = f
}
