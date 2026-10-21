package cmd

import "sigs.k8s.io/controller-runtime/pkg/client"

var UpdateStatus = updateStatus

var WorkCreatorCmd = workCreatorCmd

func UseClient(k8sClient client.Client) {
	getClient = func() (client.Client, error) {
		return k8sClient, nil
	}
}
