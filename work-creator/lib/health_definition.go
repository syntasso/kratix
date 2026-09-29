package lib

import (
	"bufio"
	"bytes"
	goerr "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
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
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(content)))
	for {
		raw, err := reader.Read()
		if goerr.Is(err, io.EOF) {
			return documents, true
		}
		if err != nil {
			return nil, false
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var document any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return nil, false
		}
		documents = append(documents, document)
	}
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
