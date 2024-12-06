package v1alpha1

import (
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func SetClientSet(cs clientset.Interface) {
	k8sClientSet = cs
}

func SetClient(c client.Client) {
	k8sClient = c
}

func SetPromiseFetcher(pf platformv1alpha1.PromiseFetcher) {
	promiseFetcher = pf
}
