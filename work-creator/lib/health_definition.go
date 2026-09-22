package lib

import (
	"bytes"
	goerr "errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

// HealthDefinitionsMarkerFile is written under <rootDirectory>/metadata when
// at least one HealthDefinition was stamped with the Promise version. The
// status-writer reads it to record which version is being health-checked.
const HealthDefinitionsMarkerFile = "health-definitions.yaml"

type HealthDefinitionsMarker struct {
	PromiseVersion string `json:"promiseVersion"`
}

// ReadHealthDefinitionsMarker returns the marker at markerFile. found is
// false, with no error, when the file does not exist.
func ReadHealthDefinitionsMarker(markerFile string) (marker *HealthDefinitionsMarker, found bool, err error) {
	content, err := os.ReadFile(markerFile)
	if goerr.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read %s: %w", HealthDefinitionsMarkerFile, err)
	}
	marker = &HealthDefinitionsMarker{}
	if err := yaml.Unmarshal(content, marker); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal %s: %w", HealthDefinitionsMarkerFile, err)
	}
	return marker, true, nil
}

const (
	healthDefinitionAPIVersion = "platform.kratix.io/v1alpha1"
	healthDefinitionKind       = "HealthDefinition"
)

// healthDefinitionStamper sets spec.promiseVersion on every HealthDefinition
// document it sees. A nil stamper (unversioned Promise) leaves content alone.
type healthDefinitionStamper struct {
	promiseVersion string
	stamped        int
}

func newHealthDefinitionStamper(promiseVersion string) *healthDefinitionStamper {
	if promiseVersion == "" || promiseVersion == v1alpha1.UnversionedPromiseVersion {
		return nil
	}
	return &healthDefinitionStamper{promiseVersion: promiseVersion}
}

// stamp re-marshals each HealthDefinition document in content with
// spec.promiseVersion set; every other byte is copied verbatim.
func (s *healthDefinitionStamper) stamp(content []byte) ([]byte, error) {
	if s == nil {
		return content, nil
	}

	var out bytes.Buffer
	for _, segment := range splitDocuments(content) {
		object, ok := parseHealthDefinition(segment)
		if !ok {
			out.Write(segment)
			continue
		}
		stamped, err := s.stampDocument(object)
		if err != nil {
			return nil, err
		}
		out.Write(stamped)
	}
	return out.Bytes(), nil
}

func (s *healthDefinitionStamper) stampDocument(object map[string]any) ([]byte, error) {
	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	spec["promiseVersion"] = s.promiseVersion
	object["spec"] = spec
	s.stamped++
	return yaml.Marshal(object)
}

// writeMarker records the stamped version so the status-writer can find it.
func (s *healthDefinitionStamper) writeMarker(rootDirectory string) error {
	if s == nil || s.stamped == 0 {
		return nil
	}
	marker, err := yaml.Marshal(HealthDefinitionsMarker{PromiseVersion: s.promiseVersion})
	if err != nil {
		return err
	}
	if err := os.WriteFile(healthDefinitionsMarkerPath(rootDirectory), marker, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", HealthDefinitionsMarkerFile, err)
	}
	return nil
}

// removeHealthDefinitionsMarker clears any marker left by a previous step so
// that its presence always means this run stamped a HealthDefinition.
func removeHealthDefinitionsMarker(rootDirectory string) error {
	err := os.Remove(healthDefinitionsMarkerPath(rootDirectory))
	if err != nil && !goerr.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func healthDefinitionsMarkerPath(rootDirectory string) string {
	return filepath.Join(rootDirectory, "metadata", HealthDefinitionsMarkerFile)
}

// parseHealthDefinition returns the document as a map when its apiVersion and
// kind match exactly and its spec, if set, is a mapping.
func parseHealthDefinition(document []byte) (map[string]any, bool) {
	var object map[string]any
	if err := yaml.Unmarshal(document, &object); err != nil {
		return nil, false
	}
	if object["apiVersion"] != healthDefinitionAPIVersion || object["kind"] != healthDefinitionKind {
		return nil, false
	}
	if spec, set := object["spec"]; set && spec != nil {
		if _, ok := spec.(map[string]any); !ok {
			return nil, false
		}
	}
	return object, true
}

// splitDocuments cuts content into document bodies and separator lines, in
// order, so that concatenating the segments reproduces content exactly.
func splitDocuments(content []byte) [][]byte {
	var segments [][]byte
	start := 0
	for lineStart := 0; lineStart < len(content); {
		lineEnd := len(content)
		if i := bytes.IndexByte(content[lineStart:], '\n'); i >= 0 {
			lineEnd = lineStart + i + 1
		}
		if isDocumentSeparator(content[lineStart:lineEnd]) {
			if lineStart > start {
				segments = append(segments, content[start:lineStart])
			}
			segments = append(segments, content[lineStart:lineEnd])
			start = lineEnd
		}
		lineStart = lineEnd
	}
	if start < len(content) {
		segments = append(segments, content[start:])
	}
	return segments
}

// isDocumentSeparator reports whether line is "---" followed by nothing,
// whitespace or a comment. "--- |" and "--- !!tag" open a document instead.
func isDocumentSeparator(line []byte) bool {
	line = bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(line, []byte("---")) {
		return false
	}
	rest := bytes.TrimLeft(line[3:], " \t")
	return len(rest) == 0 || rest[0] == '#'
}
