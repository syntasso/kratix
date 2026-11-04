package v1alpha1_test

import (
	"context"
	"fmt"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newFakeClientWithStateStoreRefIndex() client.Client {
	Expect(v1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	return clientfake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithIndex(&v1alpha1.Destination{}, "stateStoreRef", func(obj client.Object) []string {
			dst := obj.(*v1alpha1.Destination)
			return []string{fmt.Sprintf("%s.%s", dst.Spec.StateStoreRef.Kind, dst.Spec.StateStoreRef.Name)}
		}).
		WithInterceptorFuncs(interceptor.Funcs{List: listInReverseOrder}).
		Build()
}

// listInReverseOrder stops a spec passing only because the fake client lists by name, which a real cache does not.
func listInReverseOrder(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.List(ctx, list, opts...); err != nil {
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	slices.Reverse(items)
	return meta.SetList(list, items)
}

func createStateStore(ctx context.Context, c client.Client, kind, name string, delivery *v1alpha1.Delivery) {
	meta := metav1.ObjectMeta{Name: name}
	switch kind {
	case "GitStateStore":
		Expect(c.Create(ctx, &v1alpha1.GitStateStore{
			ObjectMeta: meta,
			Spec:       v1alpha1.GitStateStoreSpec{StateStoreCoreFields: v1alpha1.StateStoreCoreFields{Delivery: delivery}},
		})).To(Succeed())
	case "BucketStateStore":
		Expect(c.Create(ctx, &v1alpha1.BucketStateStore{
			ObjectMeta: meta,
			Spec:       v1alpha1.BucketStateStoreSpec{StateStoreCoreFields: v1alpha1.StateStoreCoreFields{Delivery: delivery}},
		})).To(Succeed())
	default:
		Fail("unknown state store kind " + kind)
	}
}

func createDestination(ctx context.Context, c client.Client, name, filepathMode string, ref *v1alpha1.StateStoreReference) {
	Expect(c.Create(ctx, &v1alpha1.Destination{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.DestinationSpec{
			Path:          name,
			StateStoreRef: ref,
			Filepath:      v1alpha1.Filepath{Mode: filepathMode},
		},
	})).To(Succeed())
}
