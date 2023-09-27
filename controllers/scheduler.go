package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Scheduler struct {
	Client client.Client
	Log    logr.Logger
}

// Only reconciles Works that are from a Promise Dependency
func (r *Scheduler) ReconcileDestination() error {
	works := v1alpha1.WorkList{}
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

func (r *Scheduler) UpdateWorkPlacement(work *v1alpha1.Work, workPlacement *v1alpha1.WorkPlacement) error {
	misscheduled := true
	for _, dest := range r.getTargetDestinationNames(work, 0) {
		if dest == workPlacement.Spec.TargetDestinationName {
			misscheduled = false
			break
		}
	}

	if misscheduled {
		workPlacement.SetMisscheduledLabel()
	}

	if len(work.Spec.WorkloadGroups) != 0 {
		workPlacement.Spec.Workloads = work.Spec.WorkloadGroups[0].Workloads
	}
	if err := r.Client.Update(context.Background(), workPlacement); err != nil {
		r.Log.Error(err, "Error updating WorkPlacement", "workplacement", workPlacement.Name)
		return err
	}

	if err := r.updateStatus(workPlacement, misscheduled); err != nil {
		return err
	}

	r.Log.Info("Successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return nil
}

func (r *Scheduler) ReconcileWork(work *v1alpha1.Work) error {
	existingWorkplacements, err := r.getExistingWorkplacementsForWork(*work)
	if err != nil {
		return err
	}

	destinationsMap := map[string]bool{}

	/* Assume all workplacements are misscheduled */
	for _, existingWorkplacement := range existingWorkplacements.Items {
		dest := existingWorkplacement.Spec.TargetDestinationName
		destinationsMap[dest] = true // true means misscheduled
	}

	// TODO: deal with updates with multiple workload groups
	if work.IsResourceRequest() && len(existingWorkplacements.Items) > 0 {
		var errored int
		for _, existingWorkplacement := range existingWorkplacements.Items {
			r.Log.Info("found workplacement for work; will try an update")
			if err := r.UpdateWorkPlacement(work, &existingWorkplacement); err != nil {
				r.Log.Error(err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name)
				errored++
			}
		}

		if errored > 0 {
			return fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements.Items))
		}
		return nil
	}

	workPlacementList := &v1alpha1.WorkPlacementList{}
	for i := range work.Spec.WorkloadGroups {
		r.Log.Info("gathering available destinations for workload", "work", work.GetName(), "workloadIndex", i)

		targetDestinationNames := r.getTargetDestinationNames(work, i)
		if len(targetDestinationNames) == 0 {
			r.Log.Info("no Destinations can be selected for scheduling",
				"destinationSelectors", work.Spec.DestinationSelectors,
				"destinationSelectorsOverride", work.Spec.WorkloadGroups[i].DestinationSelectorsOverride,
			)

			return fmt.Errorf("no Destinations can be selected for scheduling")
		}

		for _, dest := range targetDestinationNames {
			// mark the matching destinations as no longer misscheduled
			destinationsMap[dest] = false
		}

		r.Log.Info("found available target Destinations", "work", work.GetName(), "workloadIndex", i, "destinations", targetDestinationNames)

		workPlacementList.MergeWorkloads(
			v1alpha1.NewWorkplacementListForWork(work, i, targetDestinationNames),
		)
	}

	workPlacementList.Merge(existingWorkplacements)

	return r.reconcileWorkplacements(work, workPlacementList.GroupByDestinationName(), destinationsMap)
}

func (r *Scheduler) reconcileWorkplacements(work *v1alpha1.Work, workPlacementMap map[string]v1alpha1.WorkPlacement, targetDestinationMap map[string]bool) error {
	for destinationName, misscheduled := range targetDestinationMap {
		targetWp := workPlacementMap[destinationName]
		workPlacement := &v1alpha1.WorkPlacement{
			ObjectMeta: targetWp.ObjectMeta,
		}

		r.Log.Info("reconciling workplacement", "workplacement", workPlacement.Name, "misscheduled", misscheduled, "workloads", workPlacement.Spec.Workloads)

		op, err := controllerutil.CreateOrUpdate(context.Background(), r.Client, workPlacement, func() error {
			workPlacement.Spec = targetWp.Spec
			workPlacement.Labels = targetWp.Labels

			if misscheduled {
				workPlacement.SetMisscheduledLabel()
			}
			controllerutil.AddFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
			if err := controllerutil.SetControllerReference(work, workPlacement, scheme.Scheme); err != nil {
				r.Log.Error(err, "Error setting controller reference for WorkPlacement", "workplacement", workPlacement.Name)
				return err
			}
			return nil
		})

		if err != nil {
			return err
		}

		if err := r.updateStatus(workPlacement, misscheduled); err != nil {
			return err
		}
		r.Log.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", workPlacement.Spec.TargetDestinationName)
	}
	return nil
}

func (r *Scheduler) getExistingWorkplacementsForWork(work v1alpha1.Work) (*v1alpha1.WorkPlacementList, error) {
	workPlacementList := &v1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: work.GetNamespace(),
	}
	workSelectorLabel := labels.FormatLabels(map[string]string{
		v1alpha1.WorkLabelKey: work.Name,
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

	return workPlacementList, nil
}

func (r *Scheduler) updateStatus(workPlacement *v1alpha1.WorkPlacement, misscheduled bool) error {
	updatedWorkPlacement := &v1alpha1.WorkPlacement{}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(workPlacement), updatedWorkPlacement); err != nil {
		return err
	}

	updatedWorkPlacement.Status.Conditions = nil
	if misscheduled {
		updatedWorkPlacement.Status.Conditions = []v1.Condition{
			{
				Message:            "Target destination no longer matches destinationSelectors",
				Reason:             "DestinationSelectorMismatch",
				Type:               "Misscheduled",
				Status:             "True",
				LastTransitionTime: v1.NewTime(time.Now()),
			},
		}
	}

	return r.Client.Status().Update(context.Background(), updatedWorkPlacement)
}

// Where Work is a Resource Request return one random Destination name, where Work is a
// DestinationWorkerResource return all Destination names
func (r *Scheduler) getTargetDestinationNames(work *v1alpha1.Work, workloadIndex int) []string {
	var parsedLabels *labels.Selector
	var selectorLabels map[string]string

	if work.HasScheduling() {
		selectorLabels = work.GetSchedulingSelectors()
	}

	workloadGroup := work.Spec.WorkloadGroups[workloadIndex]
	if len(workloadGroup.DestinationSelectorsOverride) > 0 {
		// TODO: can DestinationSelectorsOverride be more than 1?
		selectorLabels = workloadGroup.DestinationSelectorsOverride[0].MatchLabels
	}

	if selectorLabels != nil {
		p, err := labels.Parse(labels.FormatLabels(selectorLabels))
		if err != nil {
			return make([]string, 0)
		}
		parsedLabels = &p
	}

	destinations := r.getDestinationsForWork(parsedLabels)

	if len(destinations) == 0 {
		return make([]string, 0)
	}

	if work.IsResourceRequest() {
		r.Log.Info("Getting Destination names for Resource Request")
		var targetDestinationNames = make([]string, 1)
		randomDestinationIndex := rand.Intn(len(destinations))
		targetDestinationNames[0] = destinations[randomDestinationIndex].Name
		r.Log.Info("Adding Destination: " + targetDestinationNames[0])
		return targetDestinationNames
	} else if work.IsDependency() {
		r.Log.Info("Getting Destination names for dependencies", "workloadIndex", workloadIndex)
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
func (r *Scheduler) getDestinationsForWork(selector *labels.Selector) []v1alpha1.Destination {
	destinations := &v1alpha1.DestinationList{}
	lo := &client.ListOptions{}

	if selector != nil {
		lo.LabelSelector = *selector
	}

	err := r.Client.List(context.Background(), destinations, lo)
	if err != nil {
		r.Log.Error(err, "Error listing available Destinations")
	}
	return destinations.Items
}
