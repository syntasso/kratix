//go:build enterprise

package controllers

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (r *PromiseReconciler) reconcileAllRRs(rrGVK schema.GroupVersionKind) error {
	// do our enterprise specific stuff here
	return nil
}
