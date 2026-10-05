package healthdefinition

import (
	"bytes"

	"sigs.k8s.io/yaml"
)

const (
	APIVersion = "platform.kratix.io/v1alpha1"
	Kind       = "HealthDefinition"
)

// Count returns how many HealthDefinition documents content holds; a file in
// which any document fails to parse counts 0.
func Count(content []byte) int {
	documents, ok := Decode(content)
	if !ok {
		return 0
	}
	count := 0
	for _, document := range documents {
		if Is(document) {
			count++
		}
	}
	return count
}

func Is(document any) bool {
	object, ok := document.(map[string]any)
	return ok && object["apiVersion"] == APIVersion && object["kind"] == Kind
}

// Decode parses every YAML document in content. ok is false when any
// document cannot be parsed, so the caller treats the file as opaque.
func Decode(content []byte) (documents []any, ok bool) {
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
// content ("--- {a: 1}", "--- !!map", "--- |") keeps its marker for the parser.
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
