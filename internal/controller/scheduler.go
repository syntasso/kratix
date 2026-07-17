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
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/resourceutil"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
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
	// blockedStatus means the WorkloadGroup matched one or more Destinations by
	// label, but every match denied the Promise via its SchedulingPolicy, so no
	// WorkPlacement could be created.
	blockedStatus schedulingStatus = "blockedByPolicy"
)

type Scheduler struct {
	Client        client.Client
	Log           logr.Logger
	EventRecorder events.EventRecorder
}

// Reconciles all WorkloadGroups in a Work by scheduling them to Destinations via
// Workplacements.
func (s *Scheduler) ReconcileWork(ctx context.Context, work *v1alpha1.Work) ([]string, error) {
	var unschedulable, misplaced, blockedByPolicy []string
	for _, wg := range work.Spec.WorkloadGroups {
		workloadGroupScheduleStatus, err := s.reconcileWorkloadGroup(ctx, wg, work)
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
			return nil, s.Client.Status().Update(ctx, work)
		}

		switch workloadGroupScheduleStatus {
		case unscheduledStatus:
			unschedulable = append(unschedulable, wg.ID)
		case blockedStatus:
			blockedByPolicy = append(blockedByPolicy, wg.ID)
		case misplacedStatus:
			misplaced = append(misplaced, wg.ID)
		case scheduledStatus:
		}
	}

	if s.updateWorkStatus(work, unschedulable, misplaced, blockedByPolicy) {
		if err := s.Client.Status().Update(ctx, work); err != nil {
			return nil, err
		}
	}

	// Blocked-by-policy WorkloadGroups are also pending scheduling: report them so
	// the WorkReconciler requeues (e.g. for resource requests) in case the policy
	// or the set of Destinations changes.
	pending := append(unschedulable, blockedByPolicy...)
	return pending, s.cleanupDanglingWorkplacements(ctx, work)
}

func (s *Scheduler) updateWorkStatus(w *v1alpha1.Work, unscheduledWorkloadGroupIDs, misplacedWorkloadGroupIDs, blockedByPolicyWorkloadGroupIDs []string) bool {
	var updated bool
	workPlacements := len(w.Spec.WorkloadGroups)
	notScheduled := len(unscheduledWorkloadGroupIDs) + len(blockedByPolicyWorkloadGroupIDs)
	workPlacementsCreated := workPlacements - notScheduled
	if workPlacements != w.Status.WorkPlacements || workPlacementsCreated != w.Status.WorkPlacementsCreated {
		w.Status.WorkPlacements = workPlacements
		w.Status.WorkPlacementsCreated = workPlacementsCreated
		updated = true
	}

	var readyCond, scheduleSucceededCond metav1.Condition

	if len(blockedByPolicyWorkloadGroupIDs) > 0 {
		readyCond = metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  resourceutil.BlockedByDestinationPolicyReason,
			Message: "Pending",
		}
		scheduleSucceededCond = metav1.Condition{
			Type:   "ScheduleSucceeded",
			Status: metav1.ConditionFalse,
			Reason: resourceutil.BlockedByDestinationPolicyReason,
			Message: fmt.Sprintf(
				"Promise %q not authorized by any matching destination's schedulingPolicy for workloadGroups: [%s]",
				w.Spec.PromiseName,
				strings.Join(blockedByPolicyWorkloadGroupIDs, ","),
			),
		}
	} else if len(unscheduledWorkloadGroupIDs) > 0 {
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
		scheduleSucceededCond = metav1.Condition{
			Type:               v1alpha1.ScheduleSucceededConditionType,
			Reason:             scheduleSucceededConditionMismatchReason,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Message: fmt.Sprintf(
				"Target destination no longer matches destinationSelectors for workloadGroups: [%s]",
				strings.Join(misplacedWorkloadGroupIDs, ",")),
		}
		readyCond = metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "Misplaced",
			Message: "Misplaced",
		}
		s.EventRecorder.Eventf(w, nil, corev1.EventTypeWarning, scheduleSucceededConditionMismatchReason, scheduleSucceededConditionMismatchReason, "Target destination no longer matches destinationSelectors for workloadGroups: [%s] ",
			strings.Join(misplacedWorkloadGroupIDs, ","))
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
		s.EventRecorder.Eventf(w, nil, corev1.EventTypeNormal, "AllWorkplacementsScheduled", "AllWorkplacementsScheduled", "All workplacements scheduled successfully")
	}

	if apimeta.SetStatusCondition(&w.Status.Conditions, scheduleSucceededCond) {
		apimeta.SetStatusCondition(&w.Status.Conditions, readyCond)
		updated = true
	}
	return updated
}

func (s *Scheduler) cleanupDanglingWorkplacements(ctx context.Context, work *v1alpha1.Work) error {
	workplacementsThatShouldExist := map[string]interface{}{}
	for _, wg := range work.Spec.WorkloadGroups {
		workPlacements, err := s.getExistingWorkPlacementsForWorkloadGroup(ctx, work.Namespace, work.Name, wg)
		if err != nil {
			return err
		}
		for _, wp := range workPlacements {
			workplacementsThatShouldExist[wp.Name] = nil
		}
	}

	allWorkplacementsForWork, err := s.getExistingWorkPlacementsForWork(ctx, work.Namespace, work.Name)
	if err != nil {
		return err
	}

	for _, wp := range allWorkplacementsForWork {
		if _, exists := workplacementsThatShouldExist[wp.Name]; !exists {
			logging.Debug(s.Log, "deleting workplacement that no longer references a workloadGroup", "workName", work.Name, "workPlacementName", wp.Name, "namespace", work.Namespace)
			err := s.Client.Delete(ctx, &wp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Reconciles a WorkloadGroup by scheduling it to a Destination via a Workplacement.
func (s *Scheduler) reconcileWorkloadGroup(ctx context.Context, workloadGroup v1alpha1.WorkloadGroup, work *v1alpha1.Work) (schedulingStatus, error) {
	existingWorkplacements, err := s.getExistingWorkPlacementsForWorkloadGroup(ctx, work.Namespace, work.Name, workloadGroup)
	if err != nil {
		return "", err
	}

	status := scheduledStatus
	outcome := telemetry.WorkPlacementOutcomeScheduled
	if work.IsResourceRequest() {
		// If the Work is for a Resource Request, only one Workplacement will be created per
		// WorkloadGroup. If this Workplacement already exists, it will be updated.
		if len(existingWorkplacements) > 0 {
			var errored int
			for _, existingWorkplacement := range existingWorkplacements {
				logging.Debug(s.Log, "found existing workplacement; will update")
				misplaced, err := s.updateWorkPlacement(ctx, workloadGroup, &existingWorkplacement, work.GetGeneration())
				if err != nil {
					logging.Error(s.Log, err, "error updating workplacement for work", "workplacement", existingWorkplacement.Name, "work", work.Name, "workloadGroupID", workloadGroup.ID)
					errored++
				}
				outcomeAttrs := telemetry.WorkPlacementOutcomeAttributes(
					work.Spec.PromiseName,
					work.Spec.ResourceName,
					work.GetNamespace(),
					existingWorkplacement.Spec.TargetDestinationName,
				)
				if misplaced {
					status = misplacedStatus
					outcome = telemetry.WorkPlacementOutcomeMisplaced
				}
				telemetry.RecordWorkPlacementOutcome(ctx, outcome, outcomeAttrs...)
			}

			if errored > 0 {
				return "", fmt.Errorf("failed to update %d of %d workplacements for work", errored, len(existingWorkplacements))
			}

			return status, nil
		}
	}

	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
	targetDestinationNames, deniedByPolicy, err := s.getTargetDestinationNames(ctx, destinationSelectors, work)
	if err != nil {
		return "", err
	}
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
		// Distinguish "no destination matched the labels" from "destinations matched
		// but their schedulingPolicy denied this Promise": the latter is an
		// authorization outcome the Promise author needs to see explicitly.
		if len(deniedByPolicy) > 0 {
			logging.Warn(s.Log, "promise not authorized to schedule to any matching destination",
				"promise", work.Spec.PromiseName, "deniedDestinations", deniedByPolicy, "workloadGroupID", workloadGroup.ID)
			s.EventRecorder.Eventf(work, nil, corev1.EventTypeWarning, resourceutil.BlockedByDestinationPolicyReason, resourceutil.BlockedByDestinationPolicyReason,
				"promise %q is not authorized by the schedulingPolicy of matching destination(s) [%s] for workloadGroup: %s",
				work.Spec.PromiseName, strings.Join(deniedByPolicy, ","), workloadGroup.ID)

			telemetry.RecordWorkPlacementOutcome(
				ctx,
				telemetry.WorkPlacementOutcomeUnscheduled,
				telemetry.WorkPlacementOutcomeAttributes(
					work.Spec.PromiseName,
					work.Spec.ResourceName,
					work.GetNamespace(),
					"",
				)...,
			)
			return blockedStatus, nil
		}

		logging.Warn(s.Log, "no destinations can be selected for scheduling", "scheduling", destinationSelectors, "workloadGroupDirectory", workloadGroup.Directory, "workloadGroupID", workloadGroup.ID)
		s.EventRecorder.Eventf(work, nil, corev1.EventTypeNormal, "NoMatchingDestination", "NoMatchingDestination", "waiting for a destination with labels for workloadGroup: %s", workloadGroup.ID)

		telemetry.RecordWorkPlacementOutcome(
			ctx,
			telemetry.WorkPlacementOutcomeUnscheduled,
			telemetry.WorkPlacementOutcomeAttributes(
				work.Spec.PromiseName,
				work.Spec.ResourceName,
				work.GetNamespace(),
				"",
			)...,
		)
		return unscheduledStatus, nil
	}

	logging.Debug(s.Log, "found available target destinations", "work", work.GetName(), "destinations", targetDestinationNames)
	misplaced, err := s.applyWorkplacementsForTargetDestinations(ctx, workloadGroup, work, targetDestinationMap)
	if err != nil {
		return "", err
	}

	if misplaced {
		status = misplacedStatus
	}

	return status, nil
}

func (s *Scheduler) updateWorkPlacement(ctx context.Context, workloadGroup v1alpha1.WorkloadGroup, workPlacement *v1alpha1.WorkPlacement, workGeneration int64) (bool, error) {
	misplaced := true
	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
	destinations, err := s.getDestinationsForWorkloadGroup(ctx, destinationSelectors)
	if err != nil {
		return false, err
	}
	for _, dest := range destinations {
		if dest.GetName() == workPlacement.Spec.TargetDestinationName {
			misplaced = false
			break
		}
	}

	if misplaced {
		s.labelWorkplacementAsMisplaced(workPlacement)
	}

	workPlacement.Spec.WorkGeneration = workGeneration
	workPlacement.Spec.Workloads = workloadGroup.Workloads
	if err := s.Client.Update(ctx, workPlacement); err != nil {
		logging.Error(s.Log, err, "error updating WorkPlacement", "workplacement", workPlacement.Name)
		return false, err
	}

	if err := s.updateWorkPlacementStatus(ctx, workPlacement, misplaced); err != nil {
		return false, err
	}

	logging.Info(s.Log, "successfully updated WorkPlacement workloads", "workplacement", workPlacement.Name)
	return misplaced, nil
}

func (s *Scheduler) labelWorkplacementAsMisplaced(workPlacement *v1alpha1.WorkPlacement) {
	logging.Warn(s.Log, "workplacement scheduled to destination that doesn't fulfil scheduling requirements")
	newLabels := workPlacement.GetLabels()
	if newLabels == nil {
		newLabels = make(map[string]string)
	}
	newLabels[misscheduledLabel] = "true"
	workPlacement.SetLabels(newLabels)
}

func (s *Scheduler) getExistingWorkPlacementsForWorkloadGroup(ctx context.Context, namespace, workName string, workloadGroup v1alpha1.WorkloadGroup) ([]v1alpha1.WorkPlacement, error) {
	return listWorkplacementWithLabels(ctx, s.Client, s.Log, namespace, map[string]string{
		workLabelKey:       workName,
		workloadGroupIDKey: workloadGroup.ID,
	})
}

func (s *Scheduler) getExistingWorkPlacementsForWork(ctx context.Context, namespace, workName string) ([]v1alpha1.WorkPlacement, error) {
	return listWorkplacementWithLabels(ctx, s.Client, s.Log, namespace, map[string]string{
		workLabelKey: workName,
	})
}

func listWorkplacementWithLabels(ctx context.Context, c client.Client, logger logr.Logger, namespace string, matchLabels map[string]string) ([]v1alpha1.WorkPlacement, error) {
	workPlacementList := &v1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: namespace,
	}
	workSelectorLabel := labels.FormatLabels(matchLabels)
	//<none> is valid output from above
	selector, err := labels.Parse(workSelectorLabel)
	if err != nil {
		logging.Error(logger, err, "error parsing scheduling")
		return nil, err
	}
	workPlacementListOptions.LabelSelector = selector

	logging.Trace(logger, "listing workplacements", "labels", workSelectorLabel)
	err = c.List(ctx, workPlacementList, workPlacementListOptions)
	if err != nil {
		logging.Error(logger, err, "error getting WorkPlacements")
		return nil, err
	}

	return workPlacementList.Items, nil
}

func (s *Scheduler) applyWorkplacementsForTargetDestinations(ctx context.Context, workloadGroup v1alpha1.WorkloadGroup, work *v1alpha1.Work, targetDestinationNames map[string]bool) (bool, error) {
	containsMischeduledWorkplacement := false
	for targetDestinationName, misscheduled := range targetDestinationNames {
		workPlacement := &v1alpha1.WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = work.Name + "." + targetDestinationName + "-" + shortID(workloadGroup.ID)

		op, err := controllerutil.CreateOrUpdate(ctx, s.Client, workPlacement, func() error {
			workPlacement.Spec.WorkGeneration = work.GetGeneration()
			workPlacement.Spec.Workloads = workloadGroup.Workloads
			workPlacement.Labels = map[string]string{
				workLabelKey:               work.Name,
				workloadGroupIDKey:         workloadGroup.ID,
				TargetDestinationNameLabel: targetDestinationName,
			}
			workPlacement.SetAnnotations(
				buildWorkPlacementAnnotations(
					workPlacement.GetAnnotations(),
					work.GetAnnotations(),
				),
			)

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
				logging.Error(s.Log, err, "error setting ownership")
				return err
			}
			return nil
		})

		if err != nil {
			return false, err
		}
		if err := s.updateWorkPlacementStatus(ctx, workPlacement, misscheduled); err != nil {
			return false, err
		}
		logging.Info(s.Log, "workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", work.GetName(), "destination", targetDestinationName)
		s.EventRecorder.Eventf(work, nil, corev1.EventTypeNormal, "WorkplacementReconciled", "WorkplacementReconciled", "workplacement reconciled: %s, operation: %s", workPlacement.GetName(), op)

		outcome := telemetry.WorkPlacementOutcomeScheduled
		if misscheduled {
			outcome = telemetry.WorkPlacementOutcomeMisplaced
		}
		telemetry.RecordWorkPlacementOutcome(
			ctx,
			outcome,
			telemetry.WorkPlacementOutcomeAttributes(work.Spec.PromiseName, work.Spec.ResourceName, work.GetNamespace(), targetDestinationName)...,
		)
	}
	return containsMischeduledWorkplacement, nil
}

func (s *Scheduler) updateWorkPlacementStatus(ctx context.Context, workPlacement *v1alpha1.WorkPlacement, misplaced bool) error {
	updatedwp := &v1alpha1.WorkPlacement{}
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(workPlacement), updatedwp); err != nil {
		return err
	}

	var desiredScheduleCond metav1.Condition
	if misplaced {
		desiredScheduleCond = metav1.Condition{
			Message:            scheduleSucceededConditionMismatchMsg,
			Reason:             scheduleSucceededConditionMismatchReason,
			Type:               v1alpha1.ScheduleSucceededConditionType,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Now()),
		}
		readyUpdated := apimeta.SetStatusCondition(&updatedwp.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "Misplaced",
			Message: "Misplaced",
		})
		scheduleUpdated := apimeta.SetStatusCondition(&updatedwp.Status.Conditions, desiredScheduleCond)
		s.EventRecorder.Eventf(updatedwp, nil, corev1.EventTypeWarning, scheduleSucceededConditionMismatchReason, scheduleSucceededConditionMismatchReason, "labels for destination: %s no longer match the expected labels, marking this workplacement as misplaced", updatedwp.Spec.TargetDestinationName)
		if scheduleUpdated || readyUpdated {
			return s.Client.Status().Update(ctx, updatedwp)
		}
		return nil
	}

	desiredScheduleCond = metav1.Condition{
		Message:            "Scheduled to correct Destination",
		Reason:             "ScheduledToDestination",
		Type:               v1alpha1.ScheduleSucceededConditionType,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	scheduleUpdated := apimeta.SetStatusCondition(&updatedwp.Status.Conditions, desiredScheduleCond)

	readyUpdated := false
	if scheduleUpdated {
		writeSucceededCond := apimeta.FindStatusCondition(updatedwp.Status.Conditions, v1alpha1.WriteSucceededConditionType)
		if writeSucceededCond == nil || writeSucceededCond.Status != metav1.ConditionTrue {
			readyUpdated = apimeta.SetStatusCondition(&updatedwp.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "ScheduledToDestination",
				Message: "Pending",
			})
		}
	}

	if scheduleUpdated || readyUpdated {
		return s.Client.Status().Update(ctx, updatedwp)
	}
	return nil
}

// Where Work is a Resource Request return one random Destination name, where Work is a
// DestinationWorkerResource return all Destination names
// getTargetDestinationNames returns the names of Destinations a WorkloadGroup should
// be scheduled to. It first narrows Destinations by label matching, then applies each
// candidate Destination's SchedulingPolicy to authorize (or deny) the Promise. The
// second return value lists the names of Destinations that matched by label but denied
// the Promise via policy, so the caller can distinguish "no match" from "not
// authorized".
func (s *Scheduler) getTargetDestinationNames(ctx context.Context, destinationSelectors map[string]string, work *v1alpha1.Work) ([]string, []string, error) {
	destinations, err := s.getDestinationsForWorkloadGroup(ctx, destinationSelectors)
	if err != nil {
		return nil, nil, err
	}

	if len(destinations) == 0 {
		return make([]string, 0), nil, nil
	}

	authorized, deniedByPolicy := s.authorizeDestinations(ctx, destinations, work)

	if len(authorized) == 0 {
		return make([]string, 0), deniedByPolicy, nil
	}

	if work.IsResourceRequest() {
		logging.Trace(s.Log, "getting destination names for resource request")
		var targetDestinationNames = make([]string, 1)
		randomDestinationIndex := rand.Intn(len(authorized))
		targetDestinationNames[0] = authorized[randomDestinationIndex].Name
		logging.Trace(s.Log, "adding destination", "destination", targetDestinationNames[0])
		return targetDestinationNames, deniedByPolicy, nil
	} else if work.IsDependency() {
		logging.Trace(s.Log, "getting destination names for dependencies")
		var targetDestinationNames = make([]string, len(authorized))
		for i := 0; i < len(authorized); i++ {
			targetDestinationNames[i] = authorized[i].Name
			logging.Trace(s.Log, "adding destination", "destination", targetDestinationNames[i])
		}
		return targetDestinationNames, deniedByPolicy, nil
	} else {
		logging.Trace(s.Log, "work is neither resource request nor dependency")
		return make([]string, 0), deniedByPolicy, nil
	}
}

// authorizeDestinations partitions label-matched Destinations into those that authorize
// the Work and those that deny it via their SchedulingPolicy. For resource Works the
// requester namespace's labels are resolved once (they are the same across all
// candidate Destinations) so namespace-scoped rules can be evaluated. The scheduling
// decision for each Destination is logged at debug level so the policy can be observed
// in action.
func (s *Scheduler) authorizeDestinations(ctx context.Context, destinations []v1alpha1.Destination, work *v1alpha1.Work) (authorized []v1alpha1.Destination, deniedNames []string) {
	promiseName := work.Spec.PromiseName
	isResourceWork := work.IsResourceRequest()

	var namespaceLabels map[string]string
	if isResourceWork {
		namespaceLabels = s.resolveNamespaceLabels(ctx, work.GetNamespace())
	}

	for i := range destinations {
		dest := destinations[i]
		if dest.AuthorizesWork(promiseName, isResourceWork, namespaceLabels) {
			authorized = append(authorized, dest)
			logging.Debug(s.Log, "destination authorized work via schedulingPolicy",
				"destination", dest.GetName(), "promise", promiseName, "namespace", work.GetNamespace(), "policy", schedulingPolicyForLog(dest))
		} else {
			deniedNames = append(deniedNames, dest.GetName())
			logging.Debug(s.Log, "destination denied work via schedulingPolicy",
				"destination", dest.GetName(), "promise", promiseName, "namespace", work.GetNamespace(), "policy", schedulingPolicyForLog(dest))
		}
	}
	return authorized, deniedNames
}

// resolveNamespaceLabels fetches the labels of the requester namespace for evaluating
// namespace-scoped scheduling rules. On error it logs and returns nil labels, so a
// namespace lookup failure degrades to "no labels" rather than blocking scheduling.
func (s *Scheduler) resolveNamespaceLabels(ctx context.Context, namespace string) map[string]string {
	ns := &corev1.Namespace{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		logging.Warn(s.Log, "failed to get namespace for scheduling policy evaluation; treating as no labels",
			"namespace", namespace, "error", err.Error())
		return nil
	}
	return ns.GetLabels()
}

// schedulingPolicyForLog renders a Destination's SchedulingPolicy for debug logging.
func schedulingPolicyForLog(dest v1alpha1.Destination) string {
	policy := dest.Spec.SchedulingPolicy
	if policy == nil {
		return "none"
	}
	return fmt.Sprintf("%s promiseNames=%v rules=%d", policy.Type, policy.PromiseNames, len(policy.Rules))
}

// By default, all destinations are returned. However, if scheduling is provided, only matching destinations will be returned.
func (s *Scheduler) getDestinationsForWorkloadGroup(ctx context.Context, destinationSelectors map[string]string) ([]v1alpha1.Destination, error) {
	destinationList := &v1alpha1.DestinationList{}
	lo := &client.ListOptions{}

	if len(destinationSelectors) > 0 {
		workloadGroupSelectorLabel := labels.FormatLabels(destinationSelectors)
		//<none> is valid output from above
		selector, err := labels.Parse(workloadGroupSelectorLabel)
		if err != nil {
			logging.Error(s.Log, err, "error parsing scheduling")
			return nil, err
		}
		lo.LabelSelector = selector
	}

	err := s.Client.List(ctx, destinationList, lo)
	if err != nil {
		logging.Error(s.Log, err, "error listing available destinations")
		return nil, err
	}

	destinations := []v1alpha1.Destination{}
	for _, destination := range destinationList.Items {
		if !destination.DeletionTimestamp.IsZero() ||
			(len(destinationSelectors) == 0 && (destination.Spec.StrictMatchLabels && len(destination.GetLabels()) > 0)) {
			continue
		}
		destinations = append(destinations, destination)
	}
	return destinations, nil
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
