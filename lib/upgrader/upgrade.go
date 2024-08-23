package upgrader

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Strategy interface {
	Upgrade(promise *v1alpha1.Promise) error
}

type upgradeConstructorFunc func(c client.Client, gvk schema.GroupVersionKind) Strategy

var (
	Strategies map[string]upgradeConstructorFunc = map[string]upgradeConstructorFunc{
		"allInOne": func(c client.Client, gvk schema.GroupVersionKind) Strategy {
			return &AllInOne{
				Client: c,
				gvk:    gvk,
			}
		},
	}
)

func NewStrategy(desiredUpgradeStrategy string, c client.Client, gvk schema.GroupVersionKind) (Strategy, error) {
	for upgradeStrategy, upgradeConstructor := range Strategies {
		if upgradeStrategy == desiredUpgradeStrategy {
			return upgradeConstructor(c, gvk), nil
		}
	}
	return nil, fmt.Errorf("strategy %s not found", desiredUpgradeStrategy)
}

type AllInOne struct {
	Client client.Client
	gvk    schema.GroupVersionKind
}

func (a *AllInOne) Upgrade(promise *v1alpha1.Promise) error {
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
	}
	return nil
}
