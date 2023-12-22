package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type schedulingStatus string

const (
	scheduledStatus    schedulingStatus = "scheduled"
	unscheduledStatus  schedulingStatus = "unscheduled"
	misscheduledStatus schedulingStatus = "misscheduled"
)

type Scheduler struct {
	Client client.Client
	Log    logr.Logger
}

// Reconciles all Works by scheduling each Work's WorkloadGroups to appropriate
// Destinations.
// Only reconciles Works that are from a Promise Dependency.
func (s *Scheduler) ReconcileAllDependencyWorks() error {
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

// Reconciles all WorkloadGroups in a Work by scheduling them to Destinations via
// Workplacements.
func (s *Scheduler) ReconcileWork(work *platformv1alpha1.Work) ([]string, error) {
	unschedulable := []string{}
	misscheduled := []string{}
	for _, wg := range work.Spec.WorkloadGroups {
		schedulingStatus, err := s.reconcileWorkloadGroup(wg, work)
		if err != nil {
			return nil, err
		}

		if schedulingStatus == unscheduledStatus {
			unschedulable = append(unschedulable, wg.ID)
		}

		if schedulingStatus == misscheduledStatus {
			misscheduled = append(misscheduled, wg.ID)
		}
	}

	if err := s.updateWorkStatus(work, unschedulable, misscheduled); err != nil {
		return nil, err
	}

	return unschedulable, s.cleanupDanglingWorkplacements(work)
}

func (s *Scheduler) updateWorkStatus(work *platformv1alpha1.Work, unscheduledWorkloadGroupIDs, missscheduledWorkloadGroupIDs []string) error {
	work = work.DeepCopy()
	conditions := []metav1.Condition{
		{
			//Always same
			Type:               "Scheduled",
			LastTransitionTime: v1.NewTime(time.Now()),

			//Might Change
			Status:  "True",
			Message: "All WorkloadGroups scheduled to Destination(s)",
			Reason:  "ScheduledToDestinations",
		},
		{
			//Always same
			Type:               "Misscheduled",
			LastTransitionTime: v1.NewTime(time.Now()),

			//Might Change
			Status:  "False",
			Message: "WorkGroups that have been scheduled are at the correct Destination(s)",
			Reason:  "ScheduledToCorrectDestinations",
		},
	}

	if len(unscheduledWorkloadGroupIDs) > 0 {
		conditions[0].Status = "False"
		conditions[0].Message = fmt.Sprintf("No Destinations available work WorkloadGroups: %v", unscheduledWorkloadGroupIDs)
		conditions[0].Reason = "UnscheduledWorkloadGroups"
	}

	if len(missscheduledWorkloadGroupIDs) > 0 {
		conditions[1].Status = "True"
		conditions[1].Message = fmt.Sprintf("WorkloadGroup(s) not scheduled to correct Destination(s): %v", missscheduledWorkloadGroupIDs)
		conditions[1].Reason = "ScheduledToIncorrectDestinations"
	}

	if len(work.Status.Conditions) == 2 &&
		work.Status.Conditions[0].Status == conditions[0].Status &&
		work.Status.Conditions[0].Message == conditions[0].Message &&
		work.Status.Conditions[0].Reason == conditions[0].Reason &&
		work.Status.Conditions[1].Status == conditions[1].Status &&
		work.Status.Conditions[1].Message == conditions[1].Message &&
		work.Status.Conditions[1].Reason == conditions[1].Reason {
		return nil
	}

	work.Status.Conditions = conditions
	return s.Client.Status().Update(context.Background(), work)
}

func (s *Scheduler) cleanupDanglingWorkplacements(work *platformv1alpha1.Work) error {
	workplacementsThatShouldExist := map[string]interface{}{}
	for _, wg := range work.Spec.WorkloadGroups {
		workPlacements, err := s.getExistingWorkPlacementsForWorkloadGroup(work.Namespace, work.Name, wg)
		if err != nil {
			return err
		}
		for _, wp := range workPlacements {
			workplacementsThatShouldExist[wp.Name] = nil
		}
	}

	allWorkplacementsForWork, err := s.getExistingWorkPlacementsForWork(work.Namespace, work.Name)
	if err != nil {
		return err
	}

	for _, wp := range allWorkplacementsForWork {
		if _, exists := workplacementsThatShouldExist[wp.Name]; !exists {
			s.Log.Info("deleting workplacement that no longer references a workloadGroup", "workName", work.Name, "workPlacementName", wp.Name, "namespace", work.Namespace)
			err := s.Client.Delete(context.TODO(), &wp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Reconciles a WorkloadGroup by scheduling it to a Destination via a Workplacement.
func (s *Scheduler) reconcileWorkloadGroup(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work) (schedulingStatus, error) {
	// TODO why pointer for work?

	existingWorkplacements, err := s.getExistingWorkPlacementsForWorkloadGroup(work.Namespace, work.Name, workloadGroup)
	if err != nil {
		return "", err
	}

	status := scheduledStatus
	if work.IsResourceRequest() {
		// If the Work is for a Resource Request, only one Workplacement will be created per
		// WorkloadGroup. If this Workplacement already exists, it will be updated.
		if len(existingWorkplacements) > 0 {
			var errored int
			for _, existingWorkplacement := range existingWorkplacements {
				s.Log.Info("found workplacement for work; will try an update")
				misscheduled, err := s.updateWorkPlacement(workloadGroup, work, &existingWorkplacement)
				if err != nil {
					s.Log.Error(err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name, "workloadGroupID", workloadGroup.ID)
					errored++
				}
				if misscheduled {
					status = misscheduledStatus
				}
			}

			if errored > 0 {
				return "", fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements))
			}

			return status, nil
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
		return unscheduledStatus, nil
	}

	s.Log.Info("found available target Destinations", "work", work.GetName(), "destinations", targetDestinationNames)
	misscheduled, err := s.applyWorkplacementsForTargetDestinations(workloadGroup, work, targetDestinationMap)
	if err != nil {
		return "", err
	}

	if misscheduled {
		status = misscheduledStatus
	}

	return status, nil
}

func (s *Scheduler) updateWorkPlacement(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work, workPlacement *platformv1alpha1.WorkPlacement) (bool, error) {
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
		return false, err
	}

	if err := s.updateStatus(workPlacement, misscheduled); err != nil {
		return false, err
	}

	s.Log.Info("Successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return misscheduled, nil
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
	return s.listWorkplacementWithLabels(namespace, map[string]string{
		workLabelKey:       workName,
		workloadGroupIDKey: workloadGroup.ID,
	})
}

func (s *Scheduler) getExistingWorkPlacementsForWork(namespace, workName string) ([]platformv1alpha1.WorkPlacement, error) {
	return s.listWorkplacementWithLabels(namespace, map[string]string{
		workLabelKey: workName,
	})
}

func (s *Scheduler) listWorkplacementWithLabels(namespace string, matchLabels map[string]string) ([]platformv1alpha1.WorkPlacement, error) {
	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: namespace,
	}
	workSelectorLabel := labels.FormatLabels(matchLabels)
	//<none> is valid output from above
	selector, err := labels.Parse(workSelectorLabel)

	if err != nil {
		s.Log.Error(err, "error parsing scheduling")
	}
	workPlacementListOptions.LabelSelector = selector

	s.Log.Info("Listing Workplacements", "labels", workSelectorLabel)
	err = s.Client.List(context.Background(), workPlacementList, workPlacementListOptions)
	if err != nil {
		s.Log.Error(err, "Error getting WorkPlacements")
		return nil, err
	}

	return workPlacementList.Items, nil
}

func (s *Scheduler) applyWorkplacementsForTargetDestinations(workloadGroup platformv1alpha1.WorkloadGroup, work *platformv1alpha1.Work, targetDestinationNames map[string]bool) (bool, error) {
	containsMischeduledWorkplacement := false
	for targetDestinationName, misscheduled := range targetDestinationNames {
		workPlacement := &platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName + "-" + shortID(workloadGroup.ID)

		op, err := controllerutil.CreateOrUpdate(context.Background(), s.Client, workPlacement, func() error {
			workPlacement.Spec.Workloads = workloadGroup.Workloads
			workPlacement.Labels = map[string]string{
				workLabelKey:       work.Name,
				workloadGroupIDKey: workloadGroup.ID,
			}

			if misscheduled {
				s.labelWorkplacementAsMisscheduled(workPlacement)
				containsMischeduledWorkplacement = true
			}

			workPlacement.Spec.ID = workloadGroup.ID
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
			return false, err
		}
		if err := s.updateStatus(workPlacement, misscheduled); err != nil {
			return false, err
		}
		s.Log.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", targetDestinationName)
	}
	return containsMischeduledWorkplacement, nil
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
