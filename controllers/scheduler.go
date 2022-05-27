package controllers

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Scheduler struct {
	client.Client
	Log logr.Logger
}

func (r *Scheduler) ReconcileWork(work *platformv1alpha1.Work) error {
	targetClusterNames := r.getTargetClusterNames(work)
	if len(targetClusterNames) == 0 {
		return errors.New("No Clusters registered")
	}
	return r.createWorkplacementsForTargetClusters(work.Name, targetClusterNames)
}

func (r *Scheduler) createWorkplacementsForTargetClusters(workName string, targetClusterNames []string) error {
	for _, targetClusterName := range targetClusterNames {
		workPlacement := platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = "default"
		workPlacement.Name = workName + "." + targetClusterName
		workPlacement.Spec.WorkName = workName
		workPlacement.Spec.TargetClusterName = targetClusterName
		err := r.Client.Create(context.Background(), &workPlacement)
		if err != nil {
			r.Log.Error(err, "Error creating new WorkPlacement: "+workPlacement.Name)
			return err
		}
	}
	return nil
}

// Where Work is a Resource Request return one random Cluster name, where Work is a
// ClusterWorkerResource return all Cluster names
func (r *Scheduler) getTargetClusterNames(work *platformv1alpha1.Work) []string {
	workerClusters := r.getWorkerClusters()

	if len(workerClusters) == 0 {
		return make([]string, 0)
	}

	if work.IsResourceRequest() {
		r.Log.Info("Getting Worker cluster names for Resource Request")
		var targetClusterNames = make([]string, 1)
		rand.Seed(time.Now().UnixNano())
		randomClusterIndex := rand.Intn(len(workerClusters))
		targetClusterNames[0] = workerClusters[randomClusterIndex].Name
		r.Log.Info("Adding Worker Cluster: " + targetClusterNames[0])
		return targetClusterNames
	} else if work.IsWorkerResource() {
		r.Log.Info("Getting Worker cluster names for Worker Resources")
		var targetClusterNames = make([]string, len(workerClusters))
		for i := 0; i < len(workerClusters); i++ {
			targetClusterNames[i] = workerClusters[i].Name
			r.Log.Info("Adding Worker Cluster: " + targetClusterNames[i])
		}
		return targetClusterNames
	} else {
		replicas := work.Spec.Replicas
		r.Log.Info("Cannot interpret replica count: " + fmt.Sprint(replicas))
		return make([]string, 0)
	}
}

func (r *Scheduler) getWorkerClusters() []platformv1alpha1.Cluster {
	workerClusters := &platformv1alpha1.ClusterList{}
	lo := &client.ListOptions{
		Namespace: "default",
	}
	err := r.Client.List(context.Background(), workerClusters, lo)
	if err != nil {
		r.Log.Error(err, "Error listing available clusters")
	}
	return workerClusters.Items
}
