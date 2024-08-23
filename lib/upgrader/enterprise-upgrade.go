//go:build enterprise

package upgrader

import (
	"context"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	Strategies["rolling"] = func(c client.Client, gvk schema.GroupVersionKind) Strategy {
		return &rolling{
			Client: c,
			gvk:    gvk,
		}
	}
}

type rolling struct {
	Client client.Client
	gvk    schema.GroupVersionKind
}

func (a *rolling) Upgrade(promise *v1alpha1.Promise) error {
	rrs := &unstructured.UnstructuredList{}
	rrListGVK := a.gvk
	rrListGVK.Kind = rrListGVK.Kind + "List"
	rrs.SetGroupVersionKind(rrListGVK)
	err := a.Client.List(context.Background(), rrs)
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
		if err := a.Client.Update(context.TODO(), &rr); err != nil {
			return err
		}

		time.Sleep(20 * time.Second)
		//TODO
		// check health of the resource before proceeding
	}
	return nil
}
