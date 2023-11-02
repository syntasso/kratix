package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	workLabelKey       = kratixPrefix + "work"
	workloadGroupIDKey = kratixPrefix + "workload-group-id"
	misscheduledLabel  = kratixPrefix + "misscheduled"
)

type Scheduler struct {
	Client client.Client
	Log    logr.Logger
}

// Only reconciles Works that are from a Promise Dependency
func (s *Scheduler) ReconcileDestination() error {
	works := platformv1alpha1.WorkList{}
	lo := &client.ListOptions{}
	if err := s.Client.List(context.Background(), &works, lo); err != nil {
		return err
	}

	for _, work := range works.Items {
		if work.IsDependency() {
			if _, err := s.ReconcileWork(&work); err != nil {
				s.Log.Error(err, "Failed reconciling Work: ")
			}
		}
	}

	return nil
}

func (s *Scheduler) UpdateWorkPlacement(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work, workPlacement *platformv1alpha1.WorkPlacement) error {
	misscheduled := true
	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup, work)
	for _, dest := range s.getTargetDestinationNames(destinationSelectors, work) {
		if dest == workPlacement.Spec.TargetDestinationName {
			misscheduled = false
			break
		}
	}

	if misscheduled {
		s.labelWorkplacementAsMisscheduled(workPlacement)
	}

	workPlacement.Spec.Workloads = workloadGroup.Workloads
	if err := s.Client.Update(context.Background(), workPlacement); err != nil {
		s.Log.Error(err, "Error updating WorkPlacement", "workplacement", workPlacement.Name)
		return err
	}

	if err := s.updateStatus(workPlacement, misscheduled); err != nil {
		return err
	}

	s.Log.Info("Successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return nil
}

func (s *Scheduler) ReconcileWork(work *platformv1alpha1.Work) ([]string, error) {
	unscheduable := []string{}
	for _, wg := range work.Spec.WorkloadGroups {
		scheduled, err := s.reconcileWorkloadGroup(wg, work)
		if err != nil {
			return nil, err
		}

		if !scheduled {
			unscheduable = append(unscheduable, wg.ID)
		}
	}
	return unscheduable, nil
}

// TODO why pointer for work?
func (s *Scheduler) reconcileWorkloadGroup(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work) (bool, error) {
	existingWorkplacements, err := s.getExistingWorkPlacementsForWorkloadGroup(work.Namespace, work.Name, workloadGroup)
	if err != nil {
		return false, err
	}

	if work.IsResourceRequest() {
		if len(existingWorkplacements) > 0 {
			var errored int
			for _, existingWorkplacement := range existingWorkplacements {
				s.Log.Info("found workplacement for work; will try an update")
				if err := s.UpdateWorkPlacement(workloadGroup, work, &existingWorkplacement); err != nil {
					s.Log.Error(err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name, "workloadGroupID", workloadGroup.ID)
					errored++
				}
			}

			if errored > 0 {
				return false, fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements))
			}
			return true, nil
		}
	}

	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup, work)
	targetDestinationNames := s.getTargetDestinationNames(destinationSelectors, work)
	targetDestinationMap := map[string]bool{}
	for _, dest := range targetDestinationNames {
		//false == not misscheduled
		targetDestinationMap[dest] = false
	}

	for _, existingWorkplacement := range existingWorkplacements {
		dest := existingWorkplacement.Spec.TargetDestinationName
		_, exists := targetDestinationMap[dest]
		if !exists {
			//true == misscheduled
			targetDestinationMap[dest] = true
		}
	}

	if len(targetDestinationMap) == 0 {
		s.Log.Info("no Destinations can be selected for scheduling", "scheduling", destinationSelectors, "workloadGroupDirectory", workloadGroup.Directory, "workloadGroupID", workloadGroup.ID)
		return false, nil
	}

	s.Log.Info("found available target Destinations", "work", work.GetName(), "destinations", targetDestinationNames)
	return true, s.applyWorkplacementsForTargetDestinations(workloadGroup, work, targetDestinationMap)
}

func (s *Scheduler) labelWorkplacementAsMisscheduled(workPlacement *v1alpha1.WorkPlacement) {
	s.Log.Info("Warning: WorkPlacement scheduled to destination that doesn't fufil scheduling requirements", "workplacement", workPlacement.Name, "namespace", workPlacement.Namespace)
	newLabels := workPlacement.GetLabels()
	if newLabels == nil {
		newLabels = make(map[string]string)
	}
	newLabels[misscheduledLabel] = "true"
	workPlacement.SetLabels(newLabels)
}

func misscheduledWorkPlacements(listA, listB []v1alpha1.WorkPlacement) []v1alpha1.WorkPlacement {
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

func (s *Scheduler) getExistingWorkPlacementsForWorkloadGroup(namespace, workName string, workloadGroup platformv1alpha1.WorkloadGroup) ([]platformv1alpha1.WorkPlacement, error) {
	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: namespace,
	}
	workSelectorLabel := labels.FormatLabels(map[string]string{
		workLabelKey:       workName,
		workloadGroupIDKey: workloadGroup.ID,
	})
	//<none> is valid output from above
	selector, err := labels.Parse(workSelectorLabel)

	if err != nil {
		s.Log.Error(err, "error parsing scheduling")
	}
	workPlacementListOptions.LabelSelector = selector

	s.Log.Info("Listing Workplacements for Work")
	err = s.Client.List(context.Background(), workPlacementList, workPlacementListOptions)
	if err != nil {
		s.Log.Error(err, "Error getting WorkPlacements")
		return nil, err
	}

	return workPlacementList.Items, nil
}

func (s *Scheduler) applyWorkplacementsForTargetDestinations(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work, targetDestinationNames map[string]bool) error {
	for targetDestinationName, misscheduled := range targetDestinationNames {
		workPlacement := &platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName + "-" + workloadGroup.ID[0:5]

		op, err := controllerutil.CreateOrUpdate(context.Background(), s.Client, workPlacement, func() error {
			workPlacement.Spec.Workloads = workloadGroup.Workloads
			workPlacement.Labels = map[string]string{
				workLabelKey:       work.Name,
				workloadGroupIDKey: workloadGroup.ID,
			}

			if misscheduled {
				s.labelWorkplacementAsMisscheduled(workPlacement)
			}

			workPlacement.Spec.PromiseName = work.Spec.PromiseName
			workPlacement.Spec.ResourceName = work.Spec.ResourceName
			workPlacement.Spec.TargetDestinationName = targetDestinationName
			controllerutil.AddFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)

			if err := controllerutil.SetControllerReference(work, workPlacement, scheme.Scheme); err != nil {
				s.Log.Error(err, "Error setting ownership")
				return err
			}
			return nil
		})

		if err != nil {
			return err
		}
		if err := s.updateStatus(workPlacement, misscheduled); err != nil {
			return err
		}
		s.Log.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", targetDestinationName)
	}
	return nil
}

func (s *Scheduler) updateStatus(workPlacement *platformv1alpha1.WorkPlacement, misscheduled bool) error {
	updatedWorkPlacement := &platformv1alpha1.WorkPlacement{}
	if err := s.Client.Get(context.Background(), client.ObjectKeyFromObject(workPlacement), updatedWorkPlacement); err != nil {
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

	return s.Client.Status().Update(context.Background(), updatedWorkPlacement)
}

// Where Work is a Resource Request return one random Destination name, where Work is a
// DestinationWorkerResource return all Destination names
func (s *Scheduler) getTargetDestinationNames(destinationSelectors map[string]string, work *platformv1alpha1.Work) []string {
	destinations := s.getDestinationsForWorkloadGroup(destinationSelectors)

	if len(destinations) == 0 {
		return make([]string, 0)
	}

	if work.IsResourceRequest() {
		s.Log.Info("Getting Destination names for Resource Request")
		var targetDestinationNames = make([]string, 1)
		rand.Seed(time.Now().UnixNano())
		randomDestinationIndex := rand.Intn(len(destinations))
		targetDestinationNames[0] = destinations[randomDestinationIndex].Name
		s.Log.Info("Adding Destination: " + targetDestinationNames[0])
		return targetDestinationNames
	} else if work.IsDependency() {
		s.Log.Info("Getting Destination names for dependencies")
		var targetDestinationNames = make([]string, len(destinations))
		for i := 0; i < len(destinations); i++ {
			targetDestinationNames[i] = destinations[i].Name
			s.Log.Info("Adding Destination: " + targetDestinationNames[i])
		}
		return targetDestinationNames
	} else {
		replicas := work.Spec.Replicas
		s.Log.Info("Cannot interpret replica count: " + fmt.Sprint(replicas))
		return make([]string, 0)
	}
}

// By default, all destinations are returned. However, if scheduling is provided, only matching destinations will be returned.
func (s *Scheduler) getDestinationsForWorkloadGroup(destinationSelectors map[string]string) []platformv1alpha1.Destination {
	destinations := &platformv1alpha1.DestinationList{}
	lo := &client.ListOptions{}

	if len(destinationSelectors) > 0 {
		workloadGroupSelectorLabel := labels.FormatLabels(destinationSelectors)
		//<none> is valid output from above
		selector, err := labels.Parse(workloadGroupSelectorLabel)

		if err != nil {
			s.Log.Error(err, "error parsing scheduling")
		}
		lo.LabelSelector = selector
	}

	err := s.Client.List(context.Background(), destinations, lo)
	if err != nil {
		s.Log.Error(err, "Error listing available Destinations")
	}
	return destinations.Items
}

func resolveDestinationSelectorsForWorkloadGroup(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work) map[string]string {
	sortedWorkloadGroupDestinations := sortWorkloadGroupDestinationsByLowestPriority(workloadGroup.DestinationSelectors)
	destinationSelectors := map[string]string{}

	for _, scheduling := range sortedWorkloadGroupDestinations {
		for key, value := range scheduling.MatchLabels {
			destinationSelectors[key] = value
		}
	}

	return destinationSelectors
}

// Returned in order:
// Resource-workflow, then
// Promise-workflow, then
// Promise
func sortWorkloadGroupDestinationsByLowestPriority(selector []platformv1alpha1.WorkloadGroupScheduling) []platformv1alpha1.WorkloadGroupScheduling {
	sort.SliceStable(selector, func(i, j int) bool {
		iSource := selector[i].Source
		jSource := selector[j].Source
		if iSource == "promise" {
			return false
		}

		if jSource == "promise" {
			return true
		}

		if iSource == "promise-workflow" {
			return false
		}

		if jSource == "promise-workflow" {
			return true
		}

		//if we get here both are resource-workflow, so just let i take prescedent
		return false
	})
	return selector
}
