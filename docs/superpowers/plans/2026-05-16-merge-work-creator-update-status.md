# Merge `work-creator` and `update-status` Containers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the separate `status-writer` main container in Kratix pipeline Jobs by merging the `work-creator` and `update-status` pipeline-adapter subcommands into a single new `run` subcommand that runs in one main container.

**Architecture:** Refactor the existing `updateStatus` orchestration function out of `work-creator/cmd/update_status.go` and into the `work-creator/lib` package as the exported `lib.UpdateStatus`. Add a new `pipeline-adapter run` cobra subcommand that calls `lib.WorkCreator.Execute` followed by `lib.UpdateStatus`. Update the pipeline factory (`api/v1alpha1/pipeline_factory.go`) so the Configure-workflow Job spec has a single `work-writer` main container running `pipeline-adapter run`, dropping the old `work-writer` init container and `status-writer` main container. Keep the standalone `work-creator` and `update-status` subcommands as thin wrappers over the lib functions for backward compatibility and rollback ergonomics.

**Tech Stack:** Go, cobra (CLI), controller-runtime, client-go (dynamic + typed), Ginkgo/Gomega (tests), Kubernetes Jobs.

**Spec:** `docs/superpowers/specs/2026-05-16-merge-work-creator-update-status-design.md`

---

## File Structure

**Created:**
- `work-creator/lib/status_updater.go` — new file; exported `UpdateStatus` + unexported helpers `handleWorkflowControlFile`, `addWorkflowSuspendLabel`, `readStatusFile` (moved from `cmd/update_status.go`).
- `work-creator/cmd/run.go` — new file; defines `runCmd()` which orchestrates `lib.WorkCreator.Execute` + `lib.UpdateStatus`.

**Modified:**
- `work-creator/cmd/update_status.go` — strip out the orchestration + helpers; what remains is a thin cobra command that calls `lib.UpdateStatus`.
- `work-creator/cmd/root.go` — register `runCmd()` alongside the existing three.
- `api/v1alpha1/pipeline_factory.go` — delete `workCreatorContainer()` and `statusWriterContainer()`, add `postPipelineContainer(env)`, change Configure branch of `pipelineJob()`.
- `api/v1alpha1/pipeline_types_test.go` — shift assertions from old container layout to new layout (no new test cases, only updates).
- `work-creator/README.md` — add a usage example for `pipeline-adapter run`.

**Untouched:**
- `work-creator/lib/work_creator.go` — `lib.WorkCreator.Execute` already exported.
- `work-creator/lib/reader.go` — reader container unchanged.
- `work-creator/cmd/reader.go` — unchanged.
- `work-creator/cmd/work_creator.go` — already uses `lib.WorkCreator.Execute`; no refactor needed.
- `work-creator/cmd/common.go` — `getClient()` helper unchanged.
- `work-creator/lib/helpers/helpers.go` — `Parameters`, `GetK8sClient`, `ObjectGVR` unchanged.

---

## Task 1: Move `updateStatus` orchestration into the `lib` package

**Goal:** Pull the orchestration function (currently `updateStatus` in `work-creator/cmd/update_status.go`) and its three helpers into `work-creator/lib/status_updater.go` as `lib.UpdateStatus`. After this task, `cmd/update_status.go` becomes a thin wrapper.

**Files:**
- Create: `work-creator/lib/status_updater.go`
- Modify: `work-creator/cmd/update_status.go`
- Existing test: `work-creator/lib/status_updater_test.go` (no changes; only tests pure helpers like `MergeStatuses`)

- [ ] **Step 1.1: Create `work-creator/lib/status_updater.go`**

Create the file with the full orchestration function and its helpers, moved verbatim from `cmd/update_status.go`. Export the top-level function as `UpdateStatus`.

```go
package lib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// UpdateStatus reads the pipeline-produced status.yaml from baseDir,
// merges it with the existing object's status, and applies workflow-control
// suspension/retry handling. baseDir is typically "/work-creator-files/metadata".
func UpdateStatus(ctx context.Context, baseDir string, params *helpers.Parameters, objectClient dynamic.ResourceInterface) error {
	statusFile := filepath.Join(baseDir, "status.yaml")

	existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %w", err)
	}

	existingStatus := map[string]any{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]any)
	}

	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %w", err)
	}

	if _, ok := incomingStatus["kratix"]; ok {
		return fmt.Errorf("'kratix' is a kratix managed status field that cannot be updated via workflows; " +
			"remove update to 'kratix' from the '/kratix/metadata/status.yaml' file")
	}

	mergedStatus := MergeStatuses(existingStatus, incomingStatus)

	if params.WorkflowType == v1alpha1.WorkflowTypePromise {
		if nonMessageKeys := NonMessageStatusKeys(incomingStatus); len(nonMessageKeys) > 0 {
			fmt.Fprintf(
				os.Stdout,
				"Warning: promise workflow status has unsupported keys: %s in status.yaml; only 'message' can be updated in Promise status.\n",
				strings.Join(nonMessageKeys, ", "),
			)
		}
	}

	control, err := ReadWorkflowControlFile(filepath.Join(baseDir, "workflow-control.yaml"))
	if err != nil {
		return err
	}

	if params.IsLastPipeline && !control.IfSuspendOrRetry() {
		mergedStatus = MarkAsCompleted(mergedStatus, params.WorkflowType)
	}

	existingObj, mergedStatus, err = handleWorkflowControlFile(ctx, params,
		existingObj, objectClient, mergedStatus, control)
	if err != nil {
		return err
	}

	existingObj.Object["status"] = mergedStatus

	if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	return nil
}

func handleWorkflowControlFile(ctx context.Context, params *helpers.Parameters,
	existingObj *unstructured.Unstructured, objectClient dynamic.ResourceInterface,
	mergedStatus map[string]any, control *WorkflowControl) (*unstructured.Unstructured, map[string]any, error) {
	if params.WorkflowType != v1alpha1.WorkflowTypePromise && params.WorkflowType != v1alpha1.WorkflowTypeResource {
		return existingObj, mergedStatus, nil
	}

	var err error

	if !control.IfSuspendOrRetry() {
		mergedStatus, err = ClearPipelineSuspension(mergedStatus, params.PipelineName)
		return existingObj, mergedStatus, err
	}

	retryAfterTimestamp := ""
	if control.IsRetry() {
		fmt.Fprintf(os.Stdout, "Info: workflow-control.yaml has retryAfter: %q \n", control.RetryAfter)
		after, parseErr := control.RetryDuration()
		if parseErr != nil {
			fmt.Fprintf(os.Stdout, "Error: failed to parse retryAfter duration specified in "+
				"the workflow-control.yaml file: %q \n", control.RetryAfter)
			return nil, nil, parseErr
		}
		retryAfterTimestamp = time.Now().UTC().Add(after).Format(time.RFC3339)
	}

	fmt.Fprintln(os.Stdout, "Info: workflow-control.yaml is suspending the pipeline execution; will label the object and update its pipeline execution status.")
	existingObj, err = addWorkflowSuspendLabel(ctx, objectClient, existingObj)
	if err != nil {
		return nil, nil, err
	}

	mergedStatus, err = MarkPipelineAsSuspended(mergedStatus, params.PipelineName, control.Message, retryAfterTimestamp, existingObj.GetGeneration())
	return existingObj, mergedStatus, err
}

func addWorkflowSuspendLabel(ctx context.Context, objectClient dynamic.ResourceInterface, existingObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	metadata, ok := existingObj.Object["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("existing object is missing metadata")
	}

	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
	}
	labels[v1alpha1.WorkflowSuspendedLabel] = "true"
	metadata["labels"] = labels
	existingObj.Object["metadata"] = metadata

	updatedObj, err := objectClient.Update(ctx, existingObj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update object labels: %w", err)
	}

	fmt.Fprintf(
		os.Stdout,
		"Info: labelled the object with %q label to 'true'.\n ", v1alpha1.WorkflowSuspendedLabel)
	return updatedObj, nil
}

func readStatusFile(statusFile string) (map[string]any, error) {
	incomingStatus := map[string]any{}
	if _, err := os.Stat(statusFile); err == nil {
		incomingStatusBytes, err := os.ReadFile(statusFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read status file: %w", err)
		}
		if err := yaml.Unmarshal(incomingStatusBytes, &incomingStatus); err != nil {
			return nil, fmt.Errorf("failed to unmarshal incoming status: %w", err)
		}
	}
	return incomingStatus, nil
}
```

Note the renamed identifiers: `MergeStatuses`, `NonMessageStatusKeys`, `MarkAsCompleted`, `ReadWorkflowControlFile`, `WorkflowControl`, `ClearPipelineSuspension`, `MarkPipelineAsSuspended` are already exported from the `lib` package (used by `cmd/update_status.go` today via `lib.X`). Inside the `lib` package itself we use them unqualified.

- [ ] **Step 1.2: Rewrite `work-creator/cmd/update_status.go` as a thin wrapper**

Replace the entire file with:

```go
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
)

func updateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update-status",
		Short: "Update status of Kubernetes resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runUpdateStatus(ctx)
		},
	}
}

func runUpdateStatus(ctx context.Context) error {
	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	return lib.UpdateStatus(ctx, "/work-creator-files/metadata", params, objectClient)
}
```

- [ ] **Step 1.3: Build to verify**

Run from repo root: `go build ./...`
Expected: builds cleanly. (If any other package was importing the unexported `updateStatus` from `cmd`, we'd see a failure here — there shouldn't be any.)

- [ ] **Step 1.4: Run existing tests to verify no regression**

Run from repo root:
```
go test ./work-creator/...
go test ./api/...
```
Expected: all pre-existing tests pass. `lib/status_updater_test.go` exercises `MergeStatuses` and friends which are unchanged.

- [ ] **Step 1.5: Commit**

```bash
git add work-creator/lib/status_updater.go work-creator/cmd/update_status.go
git commit -m "refactor(work-creator): move updateStatus orchestration into lib package

Extract the updateStatus function and its helpers (handleWorkflowControlFile,
addWorkflowSuspendLabel, readStatusFile) out of cmd/update_status.go into
lib/status_updater.go as the exported lib.UpdateStatus. cmd/update_status.go
becomes a thin cobra wrapper. No behaviour change; this prepares for the
upcoming 'run' subcommand that will call both lib.WorkCreator.Execute and
lib.UpdateStatus."
```

---

## Task 2: Add `pipeline-adapter run` subcommand

**Goal:** Add a new cobra subcommand that wires up the dynamic + typed clients once, runs `lib.WorkCreator.Execute`, and then runs `lib.UpdateStatus`.

**Files:**
- Create: `work-creator/cmd/run.go`
- Modify: `work-creator/cmd/root.go`

- [ ] **Step 2.1: Create `work-creator/cmd/run.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

// runCmd executes the post-pipeline steps as a single unit: build the Work
// resource from /work-creator-files (work-creator logic), then update the
// requesting object's status from /work-creator-files/metadata/status.yaml
// (update-status logic).
func runCmd() *cobra.Command {
	var inputDirectory string
	var promiseName string
	var pipelineName string
	var namespace string
	var resourceName string
	var resourceNamespace string
	var workflowType string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run post-pipeline work-creation followed by status-update",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("executing pipeline-adapter run with flags:",
				"input-directory", inputDirectory,
				"promise-name", promiseName,
				"pipeline-name", pipelineName,
				"namespace", namespace,
				"resource-name", resourceName,
				"resource-namespace", resourceNamespace,
				"workflow-type", workflowType)

			if inputDirectory == "" {
				return fmt.Errorf("must provide --input-directory")
			}
			if promiseName == "" {
				return fmt.Errorf("must provide --promise-name")
			}
			if pipelineName == "" {
				return fmt.Errorf("must provide --pipeline-name")
			}

			prefix := os.Getenv("KRATIX_LOGGER_PREFIX")
			if prefix != "" {
				ctrl.Log = ctrl.Log.WithName(prefix)
			}

			otelLogger := ctrl.Log.WithName("telemetry")
			if shutdown, err := telemetry.SetupTracerProvider(cmd.Context(), otelLogger, "kratix-work-creator", nil); err != nil {
				otelLogger.Error(err, "failed to configure OpenTelemetry tracing")
			} else {
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := shutdown(shutdownCtx); err != nil {
						otelLogger.Error(err, "failed to shutdown OpenTelemetry tracing")
					}
				}()
			}

			if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
				return fmt.Errorf("error adding v1alpha1 to scheme: %w", err)
			}

			k8sClient, err := getClient()
			if err != nil {
				return fmt.Errorf("error creating k8s client: %w", err)
			}

			workCreator := lib.WorkCreator{K8sClient: k8sClient}
			if err := workCreator.Execute(inputDirectory, promiseName, namespace, resourceName, resourceNamespace, workflowType, pipelineName); err != nil {
				return fmt.Errorf("work creator execution failed: %w", err)
			}

			ctx := context.Background()
			params := helpers.GetParametersFromEnv()

			dynClient, err := helpers.GetK8sClient()
			if err != nil {
				return fmt.Errorf("failed to create dynamic Kubernetes client: %w", err)
			}
			objectClient := dynClient.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

			if err := lib.UpdateStatus(ctx, "/work-creator-files/metadata", params, objectClient); err != nil {
				return fmt.Errorf("status update failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	cmd.Flags().StringVar(&promiseName, "promise-name", "", "Name of the promise")
	cmd.Flags().StringVar(&pipelineName, "pipeline-name", "", "Name of the Pipeline in the Workflow")
	cmd.Flags().StringVar(&namespace, "namespace", v1alpha1.SystemNamespace, "Namespace of the workflow")
	cmd.Flags().StringVar(&resourceName, "resource-name", "", "Name of the resource")
	cmd.Flags().StringVar(&resourceNamespace, "resource-namespace", "", "Namespace of the resource")
	cmd.Flags().StringVar(&workflowType, "workflow-type", "resource", "Create a Work for Promise or Resource type scheduling")

	if err := cmd.MarkFlagRequired("input-directory"); err != nil {
		log.Fatalf("error marking input-directory as required: %s", err)
	}
	if err := cmd.MarkFlagRequired("promise-name"); err != nil {
		log.Fatalf("error marking promise-name as required: %s", err)
	}
	if err := cmd.MarkFlagRequired("pipeline-name"); err != nil {
		log.Fatalf("error marking pipeline-name as required: %s", err)
	}

	return cmd
}
```

- [ ] **Step 2.2: Register `runCmd` in `work-creator/cmd/root.go`**

Replace the existing `init()` function:

```go
func init() {
	rootCmd.AddCommand(workCreatorCmd())
	rootCmd.AddCommand(updateStatusCmd())
	rootCmd.AddCommand(readerCmd())
	rootCmd.AddCommand(runCmd())
}
```

- [ ] **Step 2.3: Build to verify**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 2.4: Smoke-check the new subcommand registers**

Run: `go run ./work-creator run --help`
Expected: cobra prints the help text for the `run` subcommand listing all seven flags.

- [ ] **Step 2.5: Commit**

```bash
git add work-creator/cmd/run.go work-creator/cmd/root.go
git commit -m "feat(work-creator): add 'run' subcommand that orchestrates work-creator + update-status

The new 'pipeline-adapter run' subcommand calls lib.WorkCreator.Execute
followed by lib.UpdateStatus, so the post-pipeline steps can run in a
single container. The old 'work-creator' and 'update-status' subcommands
are unchanged; they remain shippable for manual debugging and to keep
rollback to the old container layout a one-file revert."
```

---

## Task 3: Update the pipeline factory to use a single main container

**Goal:** Replace the `work-writer` init container + `status-writer` main container with a single `work-writer` main container running `pipeline-adapter run`. This is the production-affecting change.

**Files:**
- Modify: `api/v1alpha1/pipeline_factory.go:189-256, 300-405` (the `workCreatorContainer`, `statusWriterContainer`, and `pipelineJob` functions)

- [ ] **Step 3.1: Update the unit tests first to reflect the desired new shape (TDD)**

We write the new test expectations before changing the factory so the failing tests confirm we're modifying the right code paths. The full test file is large (~2,071 lines) and contains 12 sites referencing `work-writer` or `status-writer`. Update each as below.

Modify `api/v1alpha1/pipeline_types_test.go`:

**Sites at lines 427-451 and 540-556** — two near-identical blocks asserting container ordering for promise and resource workflows. In each block:

- Change `Expect(podSpec.InitContainers).To(HaveLen(4))` to `Expect(podSpec.InitContainers).To(HaveLen(3))`.
- Remove the trailing `"work-writer"` element from the `initContainerNames` `Equal([]string{...})` matcher.
- Remove the trailing `pipelineAdapterImage` element from the `initContainerImages` `Equal([]string{...})` matcher.
- Change `Expect(podSpec.Containers[0].Name).To(Equal("status-writer"))` to `Expect(podSpec.Containers[0].Name).To(Equal("work-writer"))`.

Concretely, the first block (around line 427) becomes:

```go
Expect(podSpec.InitContainers).To(HaveLen(3))
var initContainerNames []string
var initContainerImages []string
for _, container := range podSpec.InitContainers {
    initContainerNames = append(initContainerNames, container.Name)
    initContainerImages = append(initContainerImages, container.Image)
}
Expect(initContainerNames).To(Equal([]string{
    "reader",
    pipeline.Spec.Containers[0].Name,
    pipeline.Spec.Containers[1].Name,
}))

Expect(podSpec.InitContainers[0].SecurityContext).To(Equal(defaultKratixSecurityContext))
Expect(podSpec.InitContainers[len(podSpec.InitContainers)-1].SecurityContext).To(Equal(defaultKratixSecurityContext))
Expect(podSpec.Containers[0].SecurityContext).To(Equal(defaultKratixSecurityContext))
Expect(initContainerImages).To(Equal([]string{
    pipelineAdapterImage,
    pipeline.Spec.Containers[0].Image,
    pipeline.Spec.Containers[1].Image,
}))
Expect(podSpec.Containers).To(HaveLen(1))
Expect(podSpec.Containers[0].Name).To(Equal("work-writer"))
```

Apply the same edits to the second block (around line 540).

**Sites at lines 813 and 875** — `Expect(container.Name).To(Equal("work-writer"))` assertions inside loops that fetch the container by name. These need no name change (the container is still called `work-writer`) — but verify the surrounding code still looks up the container from `InitContainers`; if so, change those lookups to `Containers`. Read the surrounding ~40 lines and adjust the source of the lookup (`podSpec.InitContainers` → `podSpec.Containers`) so the test fetches from the right slice.

**Site at line 1080** — `Expect(container.Name).To(Equal("status-writer"))`. This block asserts on the status-writer container's spec (likely env vars or resources). Since `status-writer` no longer exists, change this to `Expect(container.Name).To(Equal("work-writer"))` and verify the lookup still pulls from `podSpec.Containers[0]`. Adjust any related assertions in the surrounding ~30 lines to reflect that `work-writer` now also carries the env vars previously only on `status-writer`.

**Sites at lines 1135-1154** — two `It` blocks describing ephemeral-storage behaviour on the `status-writer` container.

- Rename the `It` descriptions: "applies the overridden ephemeral-storage requests and limits to the status-writer container" → "...to the work-writer container", and similarly for the hardcoded-default block.
- Change both `Expect(container.Name).To(Equal("status-writer"))` to `Expect(container.Name).To(Equal("work-writer"))`.
- Verify the container is fetched from `podSpec.Containers[0]` (it should already be — `status-writer` was the only main container).

**Site at line 1739** — `Expect(container.Name).To(Equal("work-writer"))`. Same as 813/875: confirm it now fetches from `Containers` not `InitContainers`.

**Site at line 438** and **547** (referenced earlier as `"work-writer"` in the `initContainerNames` slice) — already covered above.

- [ ] **Step 3.2: Run the failing tests to confirm they fail in the expected way**

Run: `go test ./api/v1alpha1/... -run TestPipeline -v`
Expected: assertion failures around container counts and names. (They fail because the factory still produces the old shape.)

- [ ] **Step 3.3: Rewrite `pipeline_factory.go` — delete the two old container builders, add `postPipelineContainer`**

In `api/v1alpha1/pipeline_factory.go`, **delete** the function `workCreatorContainer()` (currently lines ~206-256) and the function `statusWriterContainer(env)` (currently lines ~390-405).

**Add** the following function in their place:

```go
// postPipelineContainer is the single main container that runs after all
// user pipeline init containers complete. It builds the Work CR from
// pipeline output and updates the requesting object's status, replacing
// what used to be the separate work-writer init container and
// status-writer main container.
func (p *PipelineFactory) postPipelineContainer(env []corev1.EnvVar) corev1.Container {
	args := []string{
		"run",
		"--input-directory", "/work-creator-files",
		"--promise-name", p.Promise.GetName(),
		"--pipeline-name", p.Pipeline.GetName(),
		"--namespace", p.Namespace,
		"--workflow-type", string(p.WorkflowType),
	}

	if p.ResourceWorkflow {
		args = append(args, "--resource-name", p.ResourceRequest.GetName())
	}

	if p.ResourceWorkflow && p.Promise.WorkflowPipelineNamespaceSet() {
		args = append(args, "--resource-namespace", p.ResourceRequest.GetNamespace())
	}

	containerEnv := append([]corev1.EnvVar{
		{
			Name: telemetry.TraceParentEnvVar,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.annotations['%s']", telemetry.TraceParentAnnotation),
				},
			},
		},
		{
			Name: telemetry.TraceStateEnvVar,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.annotations['%s']", telemetry.TraceStateAnnotation),
				},
			},
		},
	}, env...)
	containerEnv = append(containerEnv, p.defaultEnvVars()...)

	return corev1.Container{
		Name:    "work-writer",
		Image:   os.Getenv("PIPELINE_ADAPTER_IMG"),
		Command: []string{"/bin/pipeline-adapter"},
		Args:    args,
		Env:     containerEnv,
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/work-creator-files/input", Name: "shared-output"},
			{MountPath: "/work-creator-files/metadata", Name: "shared-metadata"},
			{MountPath: "/work-creator-files/kratix-system", Name: "promise-scheduling"},
		},
		SecurityContext: kratixSecurityContext,
		ImagePullPolicy: DefaultImagePullPolicy,
		Resources:       *DefaultResourceRequirements,
	}
}
```

- [ ] **Step 3.4: Update `pipelineJob()` to use the new container in the Configure branch**

In `pipelineJob()`, locate the block (currently around lines 316-342):

```go
readerContainer := p.readerContainer()
pipelineContainers, pipelineVolumes := p.pipelineContainers()
workCreatorContainer := p.workCreatorContainer()
statusWriterContainer := p.statusWriterContainer(env)
...
initContainers = []corev1.Container{readerContainer}
switch p.WorkflowAction {
case WorkflowActionConfigure:
    initContainers = append(initContainers, pipelineContainers...)
    initContainers = append(initContainers, workCreatorContainer)
    containers = []corev1.Container{statusWriterContainer}
case WorkflowActionDelete:
    initContainers = append(initContainers, pipelineContainers[0:len(pipelineContainers)-1]...)
    containers = []corev1.Container{pipelineContainers[len(pipelineContainers)-1]}
}
```

Replace with:

```go
readerContainer := p.readerContainer()
pipelineContainers, pipelineVolumes := p.pipelineContainers()
...
initContainers = []corev1.Container{readerContainer}
switch p.WorkflowAction {
case WorkflowActionConfigure:
    initContainers = append(initContainers, pipelineContainers...)
    containers = []corev1.Container{p.postPipelineContainer(env)}
case WorkflowActionDelete:
    initContainers = append(initContainers, pipelineContainers[0:len(pipelineContainers)-1]...)
    containers = []corev1.Container{pipelineContainers[len(pipelineContainers)-1]}
}
```

Note: the local variables `workCreatorContainer` and `statusWriterContainer` are removed entirely. `postPipelineContainer` is called inline because it's only used in one place.

- [ ] **Step 3.5: Run the tests — they should now pass**

Run: `go test ./api/v1alpha1/... -v`
Expected: all tests pass, including the assertions updated in Step 3.1.

- [ ] **Step 3.6: Run the full test suite to catch any unexpected regressions**

Run from repo root: `go test ./...`
Expected: all tests pass. If `internal/controller/*` tests reference pipeline-job container shapes, investigate; the spec assumes they don't, but verify.

- [ ] **Step 3.7: Commit**

```bash
git add api/v1alpha1/pipeline_factory.go api/v1alpha1/pipeline_types_test.go
git commit -m "feat(api): merge work-writer and status-writer into single main container

Configure-workflow pipeline Jobs previously ran 'work-creator' as the final
init container and 'update-status' as the main container — two pipeline-adapter
containers back-to-back with no architectural reason for the split. Replace
both with a single 'work-writer' main container that runs 'pipeline-adapter
run', which orchestrates lib.WorkCreator.Execute followed by lib.UpdateStatus.

One fewer container per pipeline Job pod. The container name is preserved
('work-writer') to minimise churn in operator-facing logs and dashboards."
```

---

## Task 4: Update the work-creator README

**Goal:** Document the new `run` subcommand so operators discover it.

**Files:**
- Modify: `work-creator/README.md`

- [ ] **Step 4.1: Append a section for `pipeline-adapter run`**

Replace the current contents of `work-creator/README.md` with:

```markdown
* Start KinD
* `make deploy` in root of kratix
* `cd work-creator`
* Start work creator (note `--input-directory` needs to be an absolute path)
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`
* Edit samples to your liking
* Re-run work creator to update objects
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`

## Subcommands

The `pipeline-adapter` binary ships four subcommands:

* `reader` — runs before the user pipeline; fetches the requesting object and promise into `/kratix/input`.
* `work-creator` — builds a `Work` resource from pipeline output. Used by the in-cluster pipeline Job and available for manual debugging.
* `update-status` — patches the requesting object's status from `/work-creator-files/metadata/status.yaml`. Available for manual debugging.
* `run` — runs `work-creator` followed by `update-status` in a single process. **This is what the Kratix operator schedules in production**; `work-creator` and `update-status` are kept as separate subcommands purely for direct invocation when debugging a live pod.

Example (manual invocation matching what the operator schedules):

```
go run cmd/main.go run \
  --input-directory=${PWD}/test/integration/samples \
  --promise-name=yourPromise \
  --pipeline-name=yourPipeline \
  --workflow-type=resource
```
```

- [ ] **Step 4.2: Commit**

```bash
git add work-creator/README.md
git commit -m "docs(work-creator): document the new 'run' subcommand"
```

---

## Task 5: End-to-end smoke check

**Goal:** Build a fresh image, run an existing Promise through the new pipeline, and confirm a Work resource is created and the resource's status is patched. This is manual but catches things unit tests can't (trace propagation, volume mounts, env-var plumbing).

**Files:** none modified.

- [ ] **Step 5.1: Build the pipeline-adapter image**

Run from repo root: `make build-and-load-pipeline-adapter`
(If that target doesn't exist, run the equivalent `docker build` for the work-creator Dockerfile and load into KinD. Inspect `Makefile` to confirm the exact target name.)

Expected: image builds, loads into the local KinD cluster successfully.

- [ ] **Step 5.2: Deploy Kratix using the new image**

Run: `make install` (or whatever the local-deploy target is — verify from `Makefile`).
Expected: kratix-platform-system pods come up healthy.

- [ ] **Step 5.3: Apply a sample Promise and a sample resource request**

Run (from repo root):
```
kubectl apply -f config/samples/v1alpha1_promise.yaml      # or any existing sample
kubectl apply -f config/samples/v1alpha1_promise_request.yaml
```
(Use whatever sample Promise is already in `config/samples/` — pick the simplest one with a Configure workflow.)

- [ ] **Step 5.4: Inspect the resulting pipeline Job pod**

Run:
```
kubectl get pods -n kratix-platform-system -l kratix.io/workflow-action=configure
kubectl describe pod <pod-name> -n kratix-platform-system
```

Expected:
- Init containers: `reader`, plus the user pipeline containers. **No** `work-writer` in init containers.
- Containers: exactly one, named `work-writer`, with `Args: [run, --input-directory, ...]`.

- [ ] **Step 5.5: Verify the Work resource was created**

Run: `kubectl get works -A`
Expected: a Work resource named `<promise>-<resource>-<pipeline>-<hash>` appears.

- [ ] **Step 5.6: Verify the resource status was patched**

Run: `kubectl get <resource-kind> <resource-name> -o jsonpath='{.status}'`
Expected: the status object has fields populated (e.g. `message`, plus the Kratix-managed `kratix.workflow.status` indicating completion).

- [ ] **Step 5.7: Document smoke-test outcome**

If everything passes, note this in the PR description. If anything fails, file the failure as a follow-up task and investigate before proceeding.

(No commit — this is a verification task.)

---

## Self-review notes

- **Spec coverage:** All five spec sections covered — Library refactor (Task 1), new `run` command (Task 2), factory + tests (Task 3), README (Task 4), failure semantics implicitly via TDD assertions + smoke test (Task 5). Rollout is captured in the per-task commit boundaries.
- **Backward compat:** The `work-creator` and `update-status` subcommands remain wired up in `cmd/root.go` and remain shippable. Manually verified Step 1.2's update still registers `updateStatusCmd()`.
- **No drift:** All three command entry points (`work-creator`, `update-status`, `run`) call into the same `lib.WorkCreator.Execute` and `lib.UpdateStatus`. Single source of truth.
- **Rollback:** Revert the commit from Step 3.7 (one file: `api/v1alpha1/pipeline_factory.go`) and the test commit. The new `run` subcommand stays in the binary harmlessly; the old containers come back.
