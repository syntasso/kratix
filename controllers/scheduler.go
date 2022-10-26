package controllers

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Scheduler struct {
	client.Client
	Log logr.Logger
}

func (r *Scheduler) ReconcileCluster(cluster *platformv1alpha1.Cluster, scheme *runtime.Scheme) error {
	works := platformv1alpha1.WorkList{}
	lo := &client.ListOptions{
		Namespace: "default",
	}
	if err := r.Client.List(context.Background(), &works, lo); err != nil {
		return err
	}

	for _, work := range works.Items {
		if work.IsWorkerResource() {
			if err := r.ReconcileWork(&work, scheme); err != nil {
				r.Log.Error(err, "Failed reconciling Work: ")
			}
		}
	}

	return nil
}

func (r *Scheduler) ReconcileWork(work *platformv1alpha1.Work, scheme *runtime.Scheme) error {
	targetClusterNames := r.getTargetClusterNames(work)
	if len(targetClusterNames) == 0 {
		return fmt.Errorf("no Clusters can be selected for clusterSelector " + labels.FormatLabels(work.Spec.ClusterSelector))
	}
	return r.createWorkplacementsForTargetClusters(work, targetClusterNames, scheme)
}

func (r *Scheduler) createWorkplacementsForTargetClusters(work *platformv1alpha1.Work, targetClusterNames []string, scheme *runtime.Scheme) error {
	for _, targetClusterName := range targetClusterNames {
		workPlacement := platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = "default"
		workPlacement.Name = work.Name + "." + targetClusterName
		workPlacement.Spec.WorkName = work.Name
		workPlacement.Spec.TargetClusterName = targetClusterName
		controllerutil.AddFinalizer(&workPlacement, WorkPlacementFinalizer)

		if scheme != nil {
			if err := controllerutil.SetControllerReference(work, &workPlacement, scheme); err != nil {
				r.Log.Error(err, "Error setting ownership")
				return err
			}
		}

		if err := r.Client.Create(context.Background(), &workPlacement); err != nil {
			if errors.IsAlreadyExists(err) {
				continue
			}

			r.Log.Error(err, "Error creating new WorkPlacement: "+workPlacement.Name)
			return err
		}
	}
	return nil
}

// Where Work is a Resource Request return one random Cluster name, where Work is a
// ClusterWorkerResource return all Cluster names
func (r *Scheduler) getTargetClusterNames(work *platformv1alpha1.Work) []string {
	workerClusters := r.getWorkerClustersForWork(work)

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

// By default, all worker clusters are returned. However, if there are selectors provided, only matching clusters will be returned.
func (r *Scheduler) getWorkerClustersForWork(work *platformv1alpha1.Work) []platformv1alpha1.Cluster {
	workerClusters := &platformv1alpha1.ClusterList{}
	lo := &client.ListOptions{
		Namespace: "default",
	}

	if work.HasClusterSelector() {
		workSelectorLabel := labels.FormatLabels(work.Spec.ClusterSelector)
		selector, err := labels.Parse(workSelectorLabel)

		if err != nil {
			r.Log.Error(err, "error parsing cluster selector labels")
		}
		lo.LabelSelector = selector
	}

	err := r.Client.List(context.Background(), workerClusters, lo)
	if err != nil {
		r.Log.Error(err, "Error listing available clusters")
	}
	return workerClusters.Items
}
