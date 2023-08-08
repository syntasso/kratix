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
func (r *Scheduler) ReconcileDestination() error {
	works := platformv1alpha1.WorkList{}
	lo := &client.ListOptions{}
	if err := r.Client.List(context.Background(), &works, lo); err != nil {
		return err
	}

	for _, work := range works.Items {
		if work.IsDependency() {
			if err := r.ReconcileWork(&work); err != nil {
				r.Log.Error(err, "Failed reconciling Work: ")
			}
		}
	}

	return nil
}

func (r *Scheduler) ReconcileWork(work *platformv1alpha1.Work) error {
	targetDestinationNames := r.getTargetDestinationNames(work)
	if len(targetDestinationNames) == 0 {
		r.Log.Info("no Destinations can be selected for scheduling", "scheduling", work.Spec.Scheduling)
		return fmt.Errorf("no Destinations can be selected for scheduling")
	}

	r.Log.Info("found available target Destinations", "destinations", targetDestinationNames)
	return r.createWorkplacementsForTargetDestinations(work, targetDestinationNames)
}

func (r *Scheduler) createWorkplacementsForTargetDestinations(work *platformv1alpha1.Work, targetDestinationNames []string) error {
	for _, targetDestinationName := range targetDestinationNames {
		workPlacement := platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName
		workPlacement.Spec.Workload = work.Spec.Workload
		workPlacement.Spec.TargetDestinationName = targetDestinationName
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

// Where Work is a Resource Request return one random Destination name, where Work is a
// DestinationWorkerResource return all Destination names
func (r *Scheduler) getTargetDestinationNames(work *platformv1alpha1.Work) []string {
	destinations := r.getDestinationsForWork(work)

	if len(destinations) == 0 {
		return make([]string, 0)
	}

	if work.IsResourceRequest() {
		r.Log.Info("Getting Destination names for Resource Request")
		var targetDestinationNames = make([]string, 1)
		rand.Seed(time.Now().UnixNano())
		randomDestinationIndex := rand.Intn(len(destinations))
		targetDestinationNames[0] = destinations[randomDestinationIndex].Name
		r.Log.Info("Adding Destination: " + targetDestinationNames[0])
		return targetDestinationNames
	} else if work.IsDependency() {
		r.Log.Info("Getting Destination names for dependencies")
		var targetDestinationNames = make([]string, len(destinations))
		for i := 0; i < len(destinations); i++ {
			targetDestinationNames[i] = destinations[i].Name
			r.Log.Info("Adding Destination: " + targetDestinationNames[i])
		}
		return targetDestinationNames
	} else {
		replicas := work.Spec.Replicas
		r.Log.Info("Cannot interpret replica count: " + fmt.Sprint(replicas))
		return make([]string, 0)
	}
}

// By default, all destinations are returned. However, if scheduling is provided, only matching destinations will be returned.
func (r *Scheduler) getDestinationsForWork(work *platformv1alpha1.Work) []platformv1alpha1.Destination {
	destinations := &platformv1alpha1.DestinationList{}
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

	err := r.Client.List(context.Background(), destinations, lo)
	if err != nil {
		r.Log.Error(err, "Error listing available Destinations")
	}
	return destinations.Items
}
