package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"k8s.io/client-go/kubernetes/scheme"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	workLabelKey = kratixPrefix + "work"
	orphanLabel  = "kratix.io/orphaned"
)

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

func (r *Scheduler) UpdateWorkPlacement(work *platformv1alpha1.Work, workPlacement *platformv1alpha1.WorkPlacement) error {
	workPlacement.Spec.Workloads = work.Spec.Workloads
	if err := r.Client.Update(context.Background(), workPlacement); err != nil {
		r.Log.Error(err, "Error updating WorkPlacement", "workplacement", workPlacement.Name)
		return err
	}
	r.Log.Info("Successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return nil
}

func (r *Scheduler) ReconcileWork(work *platformv1alpha1.Work) error {
	existingWorkplacements, err := r.getExistingWorkplacementsForWork(*work)
	if err != nil {
		return err
	}

	if work.IsResourceRequest() {
		if len(existingWorkplacements) > 0 {
			var errored int
			for _, existingWorkplacement := range existingWorkplacements {
				r.Log.Info("found workplacement for work; will try an update")
				if err := r.UpdateWorkPlacement(work, &existingWorkplacement); err != nil {
					r.Log.Error(err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name)
					errored++
				}
			}

			if errored > 0 {
				return fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements))
			}
			return nil
		}
	}

	targetDestinationNames := r.getTargetDestinationNames(work)
	if len(targetDestinationNames) == 0 {
		r.Log.Info("no Destinations can be selected for scheduling", "scheduling", work.Spec.DestinationSelectors)
		return fmt.Errorf("no Destinations can be selected for scheduling")
	}

	r.Log.Info("found available target Destinations", "work", work.GetName(), "destinations", targetDestinationNames)
	err = r.createWorkplacementsForTargetDestinations(work, targetDestinationNames)
	if err != nil {
		return err
	}

	currentWorkplacements, err := r.getExistingWorkplacementsForWork(*work)
	if err != nil {
		return err
	}

	for _, workPlacement := range orphanedWorkPlacements(currentWorkplacements, existingWorkplacements) {
		newLabels := workPlacement.GetLabels()
		if newLabels == nil {
			newLabels = make(map[string]string)
		}
		newLabels[orphanLabel] = "true"
		workPlacement.SetLabels(newLabels)
		if err := r.Client.Update(context.TODO(), &workPlacement); err != nil {
			return err
		}
	}

	return nil
}

func orphanedWorkPlacements(listA, listB []v1alpha1.WorkPlacement) []v1alpha1.WorkPlacement {
	mb := make(map[string]struct{}, len(listB))
	for _, x := range listB {
		mb[x.GetNamespace()+"/"+x.GetName()] = struct{}{}
	}

	var diff []v1alpha1.WorkPlacement
	for _, x := range listA {
		if _, found := mb[x.GetNamespace()+"/"+x.GetName()]; !found {
			diff = append(diff, x)
		}
	}

	return diff
}

func (r *Scheduler) getExistingWorkplacementsForWork(work platformv1alpha1.Work) ([]platformv1alpha1.WorkPlacement, error) {
	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: work.GetNamespace(),
	}
	workSelectorLabel := labels.FormatLabels(map[string]string{
		workLabelKey: work.Name,
	})
	//<none> is valid output from above
	selector, err := labels.Parse(workSelectorLabel)

	if err != nil {
		r.Log.Error(err, "error parsing scheduling")
	}
	workPlacementListOptions.LabelSelector = selector

	r.Log.Info("Listing Workplacements for Work")
	err = r.Client.List(context.Background(), workPlacementList, workPlacementListOptions)
	if err != nil {
		r.Log.Error(err, "Error getting WorkPlacements")
		return nil, err
	}

	return workPlacementList.Items, nil
}

func (r *Scheduler) createWorkplacementsForTargetDestinations(work *platformv1alpha1.Work, targetDestinationNames []string) error {
	for _, targetDestinationName := range targetDestinationNames {
		workPlacement := &platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName

		op, err := controllerutil.CreateOrUpdate(context.Background(), r.Client, workPlacement, func() error {
			workPlacement.Spec.Workloads = work.Spec.Workloads
			workPlacement.Labels = map[string]string{
				workLabelKey: work.Name,
			}

			workPlacement.Spec.WorkloadCoreFields = work.Spec.WorkloadCoreFields
			workPlacement.Spec.TargetDestinationName = targetDestinationName
			controllerutil.AddFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)

			if err := controllerutil.SetControllerReference(work, workPlacement, scheme.Scheme); err != nil {
				r.Log.Error(err, "Error setting ownership")
				return err
			}
			return nil
		})

		if err != nil {
			return err
		}

		r.Log.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", targetDestinationName)
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
