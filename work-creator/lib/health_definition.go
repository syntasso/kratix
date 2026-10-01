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

// HealthDefinitionCountFile is written by the work-writer under
// <rootDirectory>/metadata on every versioned run. The status-writer reads it
// to record which version the resource now expects health results for, and
// how many HealthDefinitions the pipeline shipped. A count of zero is
// meaningful: it tells the platform that this version has no health check.
const HealthDefinitionCountFile = "health-definitions.yaml"

type HealthDefinitionCount struct {
	PromiseVersion    string `json:"promiseVersion"`
	HealthDefinitions int    `json:"healthDefinitions"`
}

// ReadHealthDefinitionCount returns the count file at path. found is false,
// with no error, when the file does not exist.
func ReadHealthDefinitionCount(path string) (count *HealthDefinitionCount, found bool, err error) {
	content, err := os.ReadFile(path)
	if goerr.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read %s: %w", HealthDefinitionCountFile, err)
	}
	count = &HealthDefinitionCount{}
	if err := yaml.Unmarshal(content, count); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal %s: %w", HealthDefinitionCountFile, err)
	}
	return count, true, nil
}

const (
	healthDefinitionAPIVersion = "platform.kratix.io/v1alpha1"
	healthDefinitionKind       = "HealthDefinition"
)

// healthDefinitionStamper sets spec.promiseVersion on every HealthDefinition
// in the pipeline output. A nil stamper (unversioned Promise) changes nothing.
type healthDefinitionStamper struct {
	promiseVersion string
	found          int
}

func newHealthDefinitionStamper(promiseVersion string) *healthDefinitionStamper {
	if promiseVersion == "" || promiseVersion == v1alpha1.UnversionedPromiseVersion {
		return nil
	}
	return &healthDefinitionStamper{promiseVersion: promiseVersion}
}

// stamp returns content unchanged unless it is a YAML file with at least one
// HealthDefinition document. In that case every document in the file is
// decoded, the HealthDefinitions get spec.promiseVersion, and the file is
// written back document by document.
func (s *healthDefinitionStamper) stamp(content []byte) ([]byte, error) {
	if s == nil {
		return content, nil
	}

	documents, ok := decodeDocuments(content)
	if !ok {
		return content, nil
	}

	stamped := 0
	for _, document := range documents {
		if object, isHealthDefinition := healthDefinition(document); isHealthDefinition {
			object["spec"].(map[string]any)["promiseVersion"] = s.promiseVersion
			stamped++
		}
	}
	if stamped == 0 {
		return content, nil
	}
	s.found += stamped

	var out bytes.Buffer
	for i, document := range documents {
		if i > 0 {
			out.WriteString("---\n")
		}
		encoded, err := yaml.Marshal(document)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
	}
	return out.Bytes(), nil
}

// decodeDocuments parses every YAML document in content. ok is false when any
// document cannot be parsed, so the caller ships the file as it is.
func decodeDocuments(content []byte) (documents []any, ok bool) {
	for _, raw := range splitDocuments(content) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var document any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return nil, false
		}
		documents = append(documents, document)
	}
	return documents, true
}

// splitDocuments cuts content at document marker lines. A marker that carries
// content ("--- {a: 1}", "--- !!map", "--- |") starts a document that keeps
// its marker, so the YAML parser sees the inline content.
func splitDocuments(content []byte) [][]byte {
	var documents [][]byte
	start := 0
	for lineStart := 0; lineStart < len(content); {
		lineEnd := len(content)
		if i := bytes.IndexByte(content[lineStart:], '\n'); i >= 0 {
			lineEnd = lineStart + i + 1
		}
		if marker, withContent := documentMarker(content[lineStart:lineEnd]); marker {
			documents = append(documents, content[start:lineStart])
			if withContent {
				start = lineStart
			} else {
				start = lineEnd
			}
		}
		lineStart = lineEnd
	}
	return append(documents, content[start:])
}

// documentMarker reports whether line starts a document ("---" followed by
// end of line or whitespace) and whether it carries content beyond a comment.
func documentMarker(line []byte) (marker, withContent bool) {
	line = bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(line, []byte("---")) {
		return false, false
	}
	if len(line) > 3 && line[3] != ' ' && line[3] != '\t' {
		return false, false
	}
	rest := bytes.TrimLeft(line[3:], " \t")
	return true, len(rest) > 0 && rest[0] != '#'
}

// healthDefinition returns the document as a map when it is a HealthDefinition
// with a spec that can take a promiseVersion. A missing spec is created.
func healthDefinition(document any) (map[string]any, bool) {
	object, ok := document.(map[string]any)
	if !ok || object["apiVersion"] != healthDefinitionAPIVersion || object["kind"] != healthDefinitionKind {
		return nil, false
	}
	switch object["spec"].(type) {
	case map[string]any:
	case nil:
		object["spec"] = map[string]any{}
	default:
		return nil, false
	}
	return object, true
}

// writeCountFile records the version and how many HealthDefinitions were
// stamped, so the status-writer can find both. Unversioned Promises write
// nothing.
func (s *healthDefinitionStamper) writeCountFile(rootDirectory string) error {
	if s == nil {
		return nil
	}
	content, err := yaml.Marshal(HealthDefinitionCount{PromiseVersion: s.promiseVersion, HealthDefinitions: s.found})
	if err != nil {
		return err
	}
	if err := os.WriteFile(healthDefinitionCountPath(rootDirectory), content, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", HealthDefinitionCountFile, err)
	}
	return nil
}

// removeHealthDefinitionCountFile clears any count file left by a previous
// step, so that its presence always means this run wrote it.
func removeHealthDefinitionCountFile(rootDirectory string) error {
	err := os.Remove(healthDefinitionCountPath(rootDirectory))
	if err != nil && !goerr.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func healthDefinitionCountPath(rootDirectory string) string {
	return filepath.Join(rootDirectory, "metadata", HealthDefinitionCountFile)
}
