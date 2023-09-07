package v1alpha1

import "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"

func SetClientSet(cs clientset.Interface) {
	clientSet = cs
}
