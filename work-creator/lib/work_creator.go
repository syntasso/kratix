package lib

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/objectutil"
	"go.uber.org/zap/zapcore"

	goerr "errors"

	"slices"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/resourceutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkCreator struct {
	K8sClient client.Client
}

func buildWorkIdentifier(promiseName, resourceName, resourceNamespace, pipelineName, workflowType string) string {
	if !strings.HasPrefix(workflowType, string(v1alpha1.WorkflowTypeResource)) {
		return fmt.Sprintf("%s-%s", promiseName, pipelineName)
	}
	if resourceNamespace != "" {
		return fmt.Sprintf("%s-%s-%s-%s", promiseName, resourceName, resourceNamespace, pipelineName)
	}
	return fmt.Sprintf("%s-%s-%s", promiseName, resourceName, pipelineName)
}

func (w *WorkCreator) Execute(rootDirectory, promiseName, namespace, resourceName, resourceNamespace, workflowType, pipelineName string) (retErr error) {
	ctx := context.Background()
	traceParent, traceState := telemetry.TraceParentFromEnv()
	extractedCtx, ok := telemetry.ContextWithTraceparent(ctx, traceParent, traceState)
	tracer := otel.Tracer("github.com/syntasso/kratix/work-creator")
	spanOpts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	if !ok {
		spanOpts = append(spanOpts, trace.WithNewRoot())
	}
	extractedCtx, span := tracer.Start(extractedCtx, "WorkCreator.Execute", spanOpts...)
	defer func() {
		if retErr != nil {
			telemetry.RecordError(span, retErr)
		}
		span.End()
	}()
	ctx = extractedCtx

	identifier := buildWorkIdentifier(promiseName, resourceName, resourceNamespace, pipelineName, workflowType)
	if namespace == "" {
		namespace = "kratix-platform-system"
	}

	opts := zap.Options{
		Development: true,
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts), func(o *zap.Options) {
		o.TimeEncoder = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05Z07:00")
	}))

	var logger = ctrl.Log.WithName("work-creator").
		WithValues("identifier", identifier).
		WithValues("workName", identifier).
		WithValues("namespace", namespace).
		WithValues("resourceName", resourceName).
		WithValues("promiseName", promiseName).
		WithValues("pipelineName", pipelineName)

	logger = telemetry.LoggerWithTrace(logger, span)
	telemetry.SetCommonAttributes(span,
		attribute.String("kratix.work.identifier", identifier),
		attribute.String("kratix.promise.name", promiseName),
		attribute.String("kratix.pipeline.name", pipelineName),
		attribute.String("kratix.workflow.type", workflowType),
		attribute.String("kratix.namespace", namespace),
	)

	workflowScheduling, err := w.getWorkflowScheduling(rootDirectory)
	if err != nil {
		return err
	}

	workflowControl, err := ReadWorkflowControlFile(filepath.Join(rootDirectory, "metadata", "workflow-control.yaml"))
	if err != nil {
		return err
	}
	if workflowControl.IfSuspendOrRetry() {
		logger.Info("workflow control requested suspension or retry; skipping Work creation or update",
			"suspend", workflowControl.Suspend,
			"retryAfter", workflowControl.RetryAfter,
		)
		return nil
	}

	var workloadGroups []v1alpha1.WorkloadGroup
	var directoriesToIgnoreForTheBaseScheduling []string
	var defaultDestinationSelectors map[string]string
	pipelineOutputDir := filepath.Join(rootDirectory, "input")

	if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
		liveLabels := resourceutil.GetWorkLabels(promiseName, resourceName, resourceNamespace, pipelineName, workflowType)
		liveWork, err := resourceutil.GetWork(w.K8sClient, namespace, liveLabels)
		if err != nil {
			return err
		}
		if err := w.writeDryRunDiff(pipelineOutputDir, liveWork); err != nil {
			return err
		}
	}

	for _, workflowDestinationSelector := range workflowScheduling {
		directory := workflowDestinationSelector.Directory
		if !isRootDirectory(directory) {
			directoriesToIgnoreForTheBaseScheduling = append(directoriesToIgnoreForTheBaseScheduling, directory)

			workloads, err := w.getWorkloadsFromDir(pipelineOutputDir, filepath.Join(pipelineOutputDir, directory), nil)

			if err != nil {
				return err
			}

			workloadGroups = append(workloadGroups, v1alpha1.WorkloadGroup{
				Workloads: workloads,
				Directory: directory,
				ID:        fmt.Sprintf("%x", md5.Sum([]byte(directory))),
				DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{
					{
						MatchLabels: workflowDestinationSelector.MatchLabels,
						Source:      workflowType + "-" + "workflow",
					},
				},
			})
		} else {
			defaultDestinationSelectors = workflowDestinationSelector.MatchLabels
		}
	}

	workloads, err := w.getWorkloadsFromDir(pipelineOutputDir, pipelineOutputDir, directoriesToIgnoreForTheBaseScheduling)
	if err != nil {
		return err
	}

	if len(workloads) > 0 {
		defaultWorkloadGroup := v1alpha1.WorkloadGroup{
			Workloads: workloads,
			Directory: v1alpha1.DefaultWorkloadGroupDirectory,
			ID:        hash.ComputeHash(v1alpha1.DefaultWorkloadGroupDirectory),
		}

		if defaultDestinationSelectors != nil {
			defaultWorkloadGroup.DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
				{
					MatchLabels: defaultDestinationSelectors,
					Source:      workflowType + "-" + "workflow",
				},
			}
		}

		destinationSelectors, err := w.getPromiseScheduling(rootDirectory)
		if err != nil {
			return err
		}

		if len(destinationSelectors) > 0 {
			var p []v1alpha1.PromiseScheduling
			var pw []v1alpha1.PromiseScheduling
			for _, selector := range destinationSelectors {
				switch selector.Source {
				case "promise":
					p = append(p, v1alpha1.PromiseScheduling{
						MatchLabels: selector.MatchLabels,
					})
				case "promise-workflow":
					pw = append(pw, v1alpha1.PromiseScheduling{
						MatchLabels: selector.MatchLabels,
					})
				}
			}

			if len(pw) > 0 {
				defaultWorkloadGroup.DestinationSelectors = append(defaultWorkloadGroup.DestinationSelectors, v1alpha1.WorkloadGroupScheduling{
					MatchLabels: v1alpha1.SquashPromiseScheduling(pw),
					Source:      "promise-workflow",
				})
			}

			if len(p) > 0 {
				defaultWorkloadGroup.DestinationSelectors = append(
					defaultWorkloadGroup.DestinationSelectors,
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: v1alpha1.SquashPromiseScheduling(p),
						Source:      "promise",
					},
				)
			}
		}

		workloadGroups = append(workloadGroups, defaultWorkloadGroup)
	}

	work := &v1alpha1.Work{}

	work.Name = objectutil.GenerateObjectName(identifier)
	work.Spec.LastExecutionTimestamp = metav1.Now()
	work.Namespace = namespace
	work.Spec.WorkloadGroups = workloadGroups
	work.Spec.PromiseName = promiseName
	work.Spec.ResourceName = resourceName
	work.SetAnnotations(telemetry.ApplyTraceAnnotations(work.GetAnnotations(), traceParent, traceState))
	work.Labels = map[string]string{}
	logger.Info("setting work labels...")

	if !strings.HasPrefix(workflowType, string(v1alpha1.WorkflowTypeResource)) {
		work.Labels = v1alpha1.GenerateSharedLabelsForPromise(promiseName)
	}

	workLabels := resourceutil.GetWorkLabels(promiseName, resourceName, resourceNamespace, pipelineName, workflowType)
	if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
		workLabels[v1alpha1.DryRunLabel] = "true"
	}

	work.SetLabels(
		labels.Merge(
			work.GetLabels(),
			workLabels,
		),
	)

	currentWork, err := resourceutil.GetWork(w.K8sClient, namespace, work.GetLabels())
	if err != nil {
		return err
	}

	if currentWork == nil {
		if err := w.K8sClient.Create(ctx, work); err != nil {
			return err
		}
		logger.Info("Work created", "workName", work.Name)
		return nil
	}

	logger.Info("Work already exists, will update")
	currentWork.Spec = work.Spec
	currentWork.SetAnnotations(telemetry.ApplyTraceAnnotations(currentWork.GetAnnotations(), traceParent, traceState))
	err = w.K8sClient.Update(ctx, currentWork)

	if err != nil {
		logger.Error(err, "Error updating Work")
		return err
	}

	logger.Info("Work updated", "workName", currentWork.Name)
	return nil
}

// /kratix/output/     /kratix/output/   "bar"
func (w *WorkCreator) getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, rootDir string, directoriesToIgnoreAtTheRootLevel []string) ([]v1alpha1.Workload, error) {
	// decompress here
	filesAndDirs, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}

	var workloads []v1alpha1.Workload

	for _, info := range filesAndDirs {
		// TODO: currently we assume everything is a file or a dir, we don't handle
		// more advanced scenarios, e.g. symlinks, file sizes, file permissions etc
		if info.IsDir() {
			if !slices.Contains(directoriesToIgnoreAtTheRootLevel, info.Name()) {
				dir := filepath.Join(rootDir, info.Name())
				newWorkloads, err := w.getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, dir, nil)
				if err != nil {
					return nil, err
				}
				workloads = append(workloads, newWorkloads...)
			}
		} else {
			filePath := filepath.Join(rootDir, info.Name())
			file, err := os.Open(filePath)
			if err != nil {
				return nil, err
			}
			byteValue, err := io.ReadAll(file)
			if err != nil {
				return nil, err
			}

			// trim /kratix/output/ from the filepath
			path, err := filepath.Rel(prefixToTrimFromWorkloadFilepath, filePath)
			if err != nil {
				return nil, err
			}

			content, err := compression.CompressContent(byteValue)
			if err != nil {
				return nil, err
			}

			workload := v1alpha1.Workload{
				Content:  string(content),
				Filepath: path,
			}

			workloads = append(workloads, workload)
		}
	}
	return workloads, nil
}

func (w *WorkCreator) getWorkflowScheduling(rootDirectory string) ([]v1alpha1.WorkflowDestinationSelectors, error) {
	destinationFile := filepath.Join(rootDirectory, "metadata", "destination-selectors.yaml")
	fileContents, err := os.ReadFile(destinationFile)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return ParseDestinationSelectors(fileContents)
}

func (w *WorkCreator) getPromiseScheduling(rootDirectory string) ([]v1alpha1.WorkloadGroupScheduling, error) {
	kratixSystemDirectory := filepath.Join(rootDirectory, "kratix-system")
	file := filepath.Join(kratixSystemDirectory, "promise-scheduling")
	fileContents, err := os.ReadFile(file)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var schedulingConfig []v1alpha1.WorkloadGroupScheduling
	err = yaml.Unmarshal(fileContents, &schedulingConfig)

	if err != nil {
		return nil, err
	}

	return schedulingConfig, nil
}

// ParseDestinationSelectors receives a slice of byte and validates the parsed destination selectors
func ParseDestinationSelectors(bytes []byte) ([]v1alpha1.WorkflowDestinationSelectors, error) {
	var schedulingConfig []v1alpha1.WorkflowDestinationSelectors
	err := yaml.Unmarshal(bytes, &schedulingConfig)

	if err != nil {
		return nil, fmt.Errorf("invalid destination-selectors.yaml: %w", err)
	}

	for i := range schedulingConfig {
		schedulingConfig[i].Directory = filepath.Clean(schedulingConfig[i].Directory)
	}

	for i := range schedulingConfig {
		if len(schedulingConfig[i].MatchLabels) == 0 {
			return nil, fmt.Errorf("invalid destination-selectors.yaml: entry with index %d has no selectors", i)
		}
	}

	if containsDuplicateScheduling(schedulingConfig) {
		err = fmt.Errorf("duplicate entries in destination-selectors.yaml: \n%v", schedulingConfig)
		return nil, err
	}

	if path, found := containsSubDirectory(schedulingConfig); found {
		return nil, fmt.Errorf("invalid directory in destination-selectors.yaml: %s, sub-directories are not allowed", path)
	}

	return schedulingConfig, nil
}

func containsSubDirectory(schedulingConfig []v1alpha1.WorkflowDestinationSelectors) (string, bool) {
	for _, selector := range schedulingConfig {
		directory := selector.Directory
		if filepath.Base(directory) != directory {
			return directory, true
		}
	}

	return "", false
}

func containsDuplicateScheduling(schedulingConfig []v1alpha1.WorkflowDestinationSelectors) bool {
	var directoriesSeen []string

	for _, selector := range schedulingConfig {
		if slices.Contains(directoriesSeen, selector.Directory) {
			return true
		}

		directoriesSeen = append(directoriesSeen, selector.Directory)
	}

	return false
}

// Assumes Dir has already been filepath.Clean'd
func isRootDirectory(dir string) bool {
	return dir == v1alpha1.DefaultWorkloadGroupDirectory
}

// writeDryRunDiff compares the new pipeline output against the previous live
// Work (if any) and writes a human-friendly markdown diff to kratix-diff.md
// inside outputDir, so it is included in the dry-run Work and lands in the
// state store alongside the normal output.
func (w *WorkCreator) writeDryRunDiff(outputDir string, liveWork *v1alpha1.Work) error {
	prevFiles, err := extractWorkFiles(liveWork)
	if err != nil {
		return fmt.Errorf("extracting files from live work: %w", err)
	}

	newFiles, err := readDirFiles(outputDir)
	if err != nil {
		return fmt.Errorf("reading output dir for diff: %w", err)
	}

	md := renderDiff(prevFiles, newFiles)
	return os.WriteFile(filepath.Join(outputDir, "kratix-diff.md"), []byte(md), 0644)
}

// extractWorkFiles decompresses all workloads from a Work into a filepath→content map.
// Returns an empty map when work is nil (first-time request).
func extractWorkFiles(work *v1alpha1.Work) (map[string]string, error) {
	files := map[string]string{}
	if work == nil {
		return files, nil
	}
	for _, group := range work.Spec.WorkloadGroups {
		for _, wl := range group.Workloads {
			content, err := compression.DecompressContent([]byte(wl.Content))
			if err != nil {
				return nil, fmt.Errorf("decompressing %s: %w", wl.Filepath, err)
			}
			files[wl.Filepath] = string(content)
		}
	}
	return files, nil
}

// readDirFiles walks dir and returns a filepath→content map using paths
// relative to dir (matching the format stored in Work workloads).
func readDirFiles(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = string(content)
		return nil
	})
	return files, err
}

// renderDiff produces a GitHub-flavoured markdown diff summary.
func renderDiff(prev, next map[string]string) string {
	delete(prev, "kratix-diff.md")
	delete(next, "kratix-diff.md")

	var added, removed, modified []string
	for path := range next {
		if _, exists := prev[path]; !exists {
			added = append(added, path)
		} else if prev[path] != next[path] {
			modified = append(modified, path)
		}
	}
	for path := range prev {
		if _, exists := next[path]; !exists {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)

	var b strings.Builder
	fmt.Fprintf(&b, "# Kratix Dry Run Diff\n\n")
	fmt.Fprintf(&b, "**%d added · %d modified · %d removed**\n", len(added), len(modified), len(removed))

	if len(added)+len(modified)+len(removed) == 0 {
		b.WriteString("\nNo changes.\n")
		return b.String()
	}

	for _, path := range added {
		fmt.Fprintf(&b, "\n---\n\n## ➕ Added: `%s`\n\n```yaml\n%s\n```\n", path, strings.TrimRight(next[path], "\n"))
	}
	for _, path := range modified {
		fmt.Fprintf(&b, "\n---\n\n## ✏️ Modified: `%s`\n\n```diff\n%s```\n", path, lineDiff(prev[path], next[path]))
	}
	for _, path := range removed {
		fmt.Fprintf(&b, "\n---\n\n## 🗑️ Removed: `%s`\n\n```yaml\n%s\n```\n", path, strings.TrimRight(prev[path], "\n"))
	}

	return b.String()
}

// lineDiff returns a unified-style diff string between two texts using
// line-level diffing from sergi/go-diff.
func lineDiff(old, new string) string {
	dmp := diffmatchpatch.New()
	a, b, lineArray := dmp.DiffLinesToChars(old, new)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	var sb strings.Builder
	for _, d := range diffs {
		lines := strings.Split(strings.TrimSuffix(d.Text, "\n"), "\n")
		prefix := " "
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			prefix = "+"
		case diffmatchpatch.DiffDelete:
			prefix = "-"
		}
		for _, line := range lines {
			fmt.Fprintf(&sb, "%s %s\n", prefix, line)
		}
	}
	return sb.String()
}
