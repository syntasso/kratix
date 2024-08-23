//go:build oss

package controllers

import (
	"context"

	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (r *PromiseReconciler) reconcileAllRRs(rrGVK schema.GroupVersionKind) error {
	//label all rr with manual reocnciliation
	rrs := &unstructured.UnstructuredList{}
	rrListGVK := rrGVK
	rrListGVK.Kind = rrListGVK.Kind + "List"
	rrs.SetGroupVersionKind(rrListGVK)
	err := r.Client.List(context.Background(), rrs)
	if err != nil {
		return err
	}
	for _, rr := range rrs.Items {
		newLabels := rr.GetLabels()
		if newLabels == nil {
			newLabels = make(map[string]string)
		}
		newLabels[resourceutil.ManualReconciliationLabel] = "true"
		rr.SetLabels(newLabels)
		if err := r.Client.Update(context.TODO(), &rr); err != nil {
			return err
		}
	}
	return nil
}
