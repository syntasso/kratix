package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"k8s.io/client-go/kubernetes/scheme"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const workLabelKey = kratixPrefix + "work"

type Scheduler struct {
	Client client.Client
	Log    logr.Logger
}

// Only reconciles Works that are from a Promise Dependency
func (r *Scheduler) ReconcileCluster() error {
	works := platformv1alpha1.WorkList{}
	lo := &client.ListOptions{}
	if err := r.Client.List(context.Background(), &works, lo); err != nil {
		return err
	}

	for _, work := range works.Items {
		if work.IsWorkerResource() {
			if err := r.ReconcileWork(&work); err != nil {
				r.Log.Error(err, "Failed reconciling Work: ")
			}
		}
	}

	return nil
}

func (r *Scheduler) ReconcileWork(work *platformv1alpha1.Work) error {
	targetClusterNames := r.getTargetClusterNames(work)
	if len(targetClusterNames) == 0 {
		r.Log.Info("no Clusters can be selected for scheduling", "scheduling", work.Spec.Scheduling)
		return fmt.Errorf("no workers can be selected for scheduling")
	}

	r.Log.Info("found available target clusters", "clusters", targetClusterNames)
	return r.createWorkplacementsForTargetClusters(work, targetClusterNames)
}

func (r *Scheduler) createWorkplacementsForTargetClusters(work *platformv1alpha1.Work, targetClusterNames []string) error {
	for _, targetClusterName := range targetClusterNames {
		workPlacement := platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetClusterName
		workPlacement.Spec.Workload = work.Spec.Workload
		workPlacement.Spec.TargetClusterName = targetClusterName
		workPlacement.ObjectMeta.Labels = map[string]string{
			workLabelKey: work.Name,
		}
		controllerutil.AddFinalizer(&workPlacement, repoCleanupWorkPlacementFinalizer)

		if err := controllerutil.SetControllerReference(work, &workPlacement, scheme.Scheme); err != nil {
			r.Log.Error(err, "Error setting ownership")
			return err
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
		r.Log.Info("Getting Worker cluster names for dependencies")
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

// By default, all workers are returned. However, if scheduling is provided, only matching workers will be returned.
func (r *Scheduler) getWorkerClustersForWork(work *platformv1alpha1.Work) []platformv1alpha1.Cluster {
	workerClusters := &platformv1alpha1.ClusterList{}
	lo := &client.ListOptions{}

	if work.HasScheduling() {
		workSelectorLabel := labels.FormatLabels(work.GetSchedulingSelectors())
		//<none> is valid output from above
		selector, err := labels.Parse(workSelectorLabel)

		if err != nil {
			r.Log.Error(err, "error parsing scheduling")
		}
		lo.LabelSelector = selector
	}

	err := r.Client.List(context.Background(), workerClusters, lo)
	if err != nil {
		r.Log.Error(err, "Error listing available workers")
	}
	return workerClusters.Items
}
