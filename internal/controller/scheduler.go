package controller

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	workLabelKey               = v1alpha1.KratixPrefix + "work"
	workloadGroupIDKey         = v1alpha1.KratixPrefix + "workload-group-id"
	misscheduledLabel          = v1alpha1.KratixPrefix + "misplaced"
	TargetDestinationNameLabel = v1alpha1.KratixPrefix + "targetDestinationName"
)

type schedulingStatus string

const (
	scheduledStatus   schedulingStatus = "scheduled"
	unscheduledStatus schedulingStatus = "unscheduled"
	misplacedStatus   schedulingStatus = "misplaced"
)

type Scheduler struct {
	Client        client.Client
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

// Reconciles all WorkloadGroups in a Work by scheduling them to Destinations via
// Workplacements.
func (s *Scheduler) ReconcileWork(work *v1alpha1.Work) ([]string, error) {
	var unschedulable, misplaced []string
	for _, wg := range work.Spec.WorkloadGroups {
		workloadGroupScheduleStatus, err := s.reconcileWorkloadGroup(wg, work)
		if err != nil {
			readyCond := metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "FailedToScheduleWorkloads",
				Message: "Pending",
			}
			scheduleSucceededCond := metav1.Condition{
				Type:    "ScheduleSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "FailedToScheduleWorkloads",
				Message: fmt.Sprintf("Failed to schedule or update for workloadGroups: [%s]", wg.ID),
			}
			apimeta.SetStatusCondition(&work.Status.Conditions, scheduleSucceededCond)
			apimeta.SetStatusCondition(&work.Status.Conditions, readyCond)
			return nil, s.Client.Status().Update(context.Background(), work)
		}

		switch workloadGroupScheduleStatus {
		case unscheduledStatus:
			unschedulable = append(unschedulable, wg.ID)
		case misplacedStatus:
			misplaced = append(misplaced, wg.ID)
		case scheduledStatus:
		}
	}

	if s.updateWorkStatus(work, unschedulable, misplaced) {
		if err := s.Client.Status().Update(context.Background(), work); err != nil {
			return nil, err
		}
	}

	return unschedulable, s.cleanupDanglingWorkplacements(work)
}

func (s *Scheduler) updateWorkStatus(w *v1alpha1.Work, unscheduledWorkloadGroupIDs, misplacedWorkloadGroupIDs []string) bool {
	var updated bool
	workPlacements := len(w.Spec.WorkloadGroups)
	workPlacementsCreated := workPlacements - len(unscheduledWorkloadGroupIDs)
	if workPlacements != w.Status.WorkPlacements || workPlacementsCreated != w.Status.WorkPlacementsCreated {
		w.Status.WorkPlacements = workPlacements
		w.Status.WorkPlacementsCreated = workPlacementsCreated
		updated = true
	}

	var readyCond, scheduleSucceededCond metav1.Condition

	if len(unscheduledWorkloadGroupIDs) > 0 {
		readyCond = metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "UnscheduledWorkloads",
			Message: "Pending",
		}
		scheduleSucceededCond = metav1.Condition{
			Type:   "ScheduleSucceeded",
			Status: metav1.ConditionFalse,
			Reason: "UnscheduledWorkloads",
			Message: fmt.Sprintf(
				"No matching destination found for workloadGroups: [%s]",
				strings.Join(unscheduledWorkloadGroupIDs, ","),
			),
		}
	} else if len(misplacedWorkloadGroupIDs) > 0 {
		scheduleSucceededCond = v1.Condition{
			Type:               scheduleSucceededConditionType,
			Reason:             scheduleSucceededConditionMismatchReason,
			Status:             v1.ConditionFalse,
			LastTransitionTime: v1.NewTime(time.Now()),
			Message: fmt.Sprintf(
				"Target destination no longer matches destinationSelectors for workloadGroups: [%s]",
				strings.Join(misplacedWorkloadGroupIDs, ",")),
		}
		readyCond = metav1.Condition{
			Type:    "Ready",
			Status:  v1.ConditionFalse,
			Reason:  "Misplaced",
			Message: "Misplaced",
		}
	} else { // all works are scheduled and none is misplaced
		readyCond = metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "AllWorkplacementsScheduled",
			Message: "Ready",
		}
		scheduleSucceededCond = metav1.Condition{
			Type:    "ScheduleSucceeded",
			Status:  metav1.ConditionTrue,
			Reason:  "AllWorkplacementsScheduled",
			Message: "All workplacements scheduled successfully",
		}
	}

	if apimeta.SetStatusCondition(&w.Status.Conditions, scheduleSucceededCond) {
		apimeta.SetStatusCondition(&w.Status.Conditions, readyCond)
		updated = true
	}
	return updated
}

func (s *Scheduler) cleanupDanglingWorkplacements(work *v1alpha1.Work) error {
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
func (s *Scheduler) reconcileWorkloadGroup(workloadGroup v1alpha1.WorkloadGroup, work *v1alpha1.Work) (schedulingStatus, error) {
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
				misplaced, err := s.updateWorkPlacement(workloadGroup, &existingWorkplacement)
				if err != nil {
					s.Log.Error(err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name, "workloadGroupID", workloadGroup.ID)
					errored++
				}
				if misplaced {
					status = misplacedStatus
				}
			}

			if errored > 0 {
				return "", fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements))
			}

			return status, nil
		}
	}

	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
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
		s.EventRecorder.Eventf(work, corev1.EventTypeNormal, "NoMatchingDestination",
			"waiting for a destination with labels for workloadGroup: %s", workloadGroup.ID)
		return unscheduledStatus, nil
	}

	s.Log.Info("found available target Destinations", "work", work.GetName(), "destinations", targetDestinationNames)
	misplaced, err := s.applyWorkplacementsForTargetDestinations(workloadGroup, work, targetDestinationMap)
	if err != nil {
		return "", err
	}

	if misplaced {
		status = misplacedStatus
	}

	return status, nil
}

func (s *Scheduler) updateWorkPlacement(workloadGroup v1alpha1.WorkloadGroup, workPlacement *v1alpha1.WorkPlacement) (bool, error) {
	misplaced := true
	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
	for _, dest := range s.getDestinationsForWorkloadGroup(destinationSelectors) {
		if dest.GetName() == workPlacement.Spec.TargetDestinationName {
			misplaced = false
			break
		}
	}

	if misplaced {
		s.labelWorkplacementAsMisplaced(workPlacement)
	}

	workPlacement.Spec.Workloads = workloadGroup.Workloads
	if err := s.Client.Update(context.Background(), workPlacement); err != nil {
		s.Log.Error(err, "Error updating WorkPlacement", "workplacement", workPlacement.Name)
		return false, err
	}

	if err := s.updateWorkPlacementStatus(workPlacement, misplaced); err != nil {
		return false, err
	}

	s.Log.Info("Successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return misplaced, nil
}

func (s *Scheduler) labelWorkplacementAsMisplaced(workPlacement *v1alpha1.WorkPlacement) {
	s.Log.Info("Warning: WorkPlacement scheduled to destination that doesn't fulfil scheduling requirements", "workplacement", workPlacement.Name, "namespace", workPlacement.Namespace)
	newLabels := workPlacement.GetLabels()
	if newLabels == nil {
		newLabels = make(map[string]string)
	}
	newLabels[misscheduledLabel] = "true"
	workPlacement.SetLabels(newLabels)
}

func (s *Scheduler) getExistingWorkPlacementsForWorkloadGroup(namespace, workName string, workloadGroup v1alpha1.WorkloadGroup) ([]v1alpha1.WorkPlacement, error) {
	return listWorkplacementWithLabels(s.Client, s.Log, namespace, map[string]string{
		workLabelKey:       workName,
		workloadGroupIDKey: workloadGroup.ID,
	})
}

func (s *Scheduler) getExistingWorkPlacementsForWork(namespace, workName string) ([]v1alpha1.WorkPlacement, error) {
	return listWorkplacementWithLabels(s.Client, s.Log, namespace, map[string]string{
		workLabelKey: workName,
	})
}

func listWorkplacementWithLabels(c client.Client, logger logr.Logger, namespace string, matchLabels map[string]string) ([]v1alpha1.WorkPlacement, error) {
	workPlacementList := &v1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: namespace,
	}
	workSelectorLabel := labels.FormatLabels(matchLabels)
	//<none> is valid output from above
	selector, err := labels.Parse(workSelectorLabel)

	if err != nil {
		logger.Error(err, "error parsing scheduling")
	}
	workPlacementListOptions.LabelSelector = selector

	logger.Info("Listing Workplacements", "labels", workSelectorLabel)
	err = c.List(context.Background(), workPlacementList, workPlacementListOptions)
	if err != nil {
		logger.Error(err, "Error getting WorkPlacements")
		return nil, err
	}

	return workPlacementList.Items, nil
}

func (s *Scheduler) applyWorkplacementsForTargetDestinations(workloadGroup v1alpha1.WorkloadGroup, work *v1alpha1.Work, targetDestinationNames map[string]bool) (bool, error) {
	containsMischeduledWorkplacement := false
	for targetDestinationName, misscheduled := range targetDestinationNames {
		workPlacement := &v1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName + "-" + shortID(workloadGroup.ID)

		op, err := controllerutil.CreateOrUpdate(context.Background(), s.Client, workPlacement, func() error {
			workPlacement.Spec.Workloads = workloadGroup.Workloads
			workPlacement.Labels = map[string]string{
				workLabelKey:               work.Name,
				workloadGroupIDKey:         workloadGroup.ID,
				TargetDestinationNameLabel: targetDestinationName,
			}
			workPlacement.SetAnnotations(work.GetAnnotations())

			workPlacement.SetPipelineName(work)

			if misscheduled {
				s.labelWorkplacementAsMisplaced(workPlacement)
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
		if err := s.updateWorkPlacementStatus(workPlacement, misscheduled); err != nil {
			return false, err
		}
		s.Log.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", targetDestinationName)
		s.EventRecorder.Eventf(work, corev1.EventTypeNormal, "WorkplacementReconciled",
			"workplacement reconciled: %s, operation: %s", workPlacement.GetName(), op)
	}
	return containsMischeduledWorkplacement, nil
}

func (s *Scheduler) updateWorkPlacementStatus(workPlacement *v1alpha1.WorkPlacement, misplaced bool) error {
	updatedwp := &v1alpha1.WorkPlacement{}
	if err := s.Client.Get(context.Background(), client.ObjectKeyFromObject(workPlacement), updatedwp); err != nil {
		return err
	}

	var desiredScheduleCond, desiredReadyCond v1.Condition
	if misplaced {
		desiredScheduleCond = v1.Condition{
			Message:            scheduleSucceededConditionMismatchMsg,
			Reason:             scheduleSucceededConditionMismatchReason,
			Type:               scheduleSucceededConditionType,
			Status:             v1.ConditionFalse,
			LastTransitionTime: v1.NewTime(time.Now()),
		}
		desiredReadyCond = metav1.Condition{
			Type:    "Ready",
			Status:  v1.ConditionFalse,
			Reason:  "Misplaced",
			Message: "Misplaced",
		}
		s.EventRecorder.Eventf(updatedwp, corev1.EventTypeWarning, scheduleSucceededConditionMismatchReason,
			"labels for destination: %s no longer match the expected labels, marking this workplacement as misplaced", updatedwp.Spec.TargetDestinationName)
	} else {
		desiredScheduleCond = v1.Condition{
			Message:            "Scheduled to correct Destination",
			Reason:             "ScheduledToDestination",
			Type:               scheduleSucceededConditionType,
			Status:             v1.ConditionTrue,
			LastTransitionTime: v1.NewTime(time.Now()),
		}
		desiredReadyCond = metav1.Condition{
			Type:    "Ready",
			Status:  v1.ConditionTrue,
			Reason:  "Ready",
			Message: "Ready",
		}
	}

	if apimeta.SetStatusCondition(&updatedwp.Status.Conditions, desiredScheduleCond) {
		apimeta.SetStatusCondition(&updatedwp.Status.Conditions, desiredReadyCond)
		return s.Client.Status().Update(context.Background(), updatedwp)
	}
	return nil
}

// Where Work is a Resource Request return one random Destination name, where Work is a
// DestinationWorkerResource return all Destination names
func (s *Scheduler) getTargetDestinationNames(destinationSelectors map[string]string, work *v1alpha1.Work) []string {
	destinations := s.getDestinationsForWorkloadGroup(destinationSelectors)

	if len(destinations) == 0 {
		return make([]string, 0)
	}

	if work.IsResourceRequest() {
		s.Log.Info("Getting Destination names for Resource Request")
		var targetDestinationNames = make([]string, 1)
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
		s.Log.Info("Work is neither resource request nor dependency")
		return make([]string, 0)
	}
}

// By default, all destinations are returned. However, if scheduling is provided, only matching destinations will be returned.
func (s *Scheduler) getDestinationsForWorkloadGroup(destinationSelectors map[string]string) []v1alpha1.Destination {
	destinationList := &v1alpha1.DestinationList{}
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

	err := s.Client.List(context.Background(), destinationList, lo)
	if err != nil {
		s.Log.Error(err, "Error listing available Destinations")
	}

	destinations := []v1alpha1.Destination{}
	for _, destination := range destinationList.Items {
		if !destination.DeletionTimestamp.IsZero() ||
			(len(destinationSelectors) == 0 && (destination.Spec.StrictMatchLabels && len(destination.GetLabels()) > 0)) {
			continue
		}
		destinations = append(destinations, destination)
	}
	return destinations
}

func resolveDestinationSelectorsForWorkloadGroup(workloadGroup v1alpha1.WorkloadGroup) map[string]string {
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
func sortWorkloadGroupDestinationsByLowestPriority(selector []v1alpha1.WorkloadGroupScheduling) []v1alpha1.WorkloadGroupScheduling {
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

		//if we get here both are resource-workflow, so just let it take precedent
		return false
	})
	return selector
}
