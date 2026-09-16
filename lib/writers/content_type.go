package writers

import (
	"path/filepath"
	"strings"
)

// ContentTypeFor maps a workload path to the content type stored with the object.
// Anything unrecognised keeps the store's own default.
func ContentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		// the type registered for YAML by RFC 9512
		return "application/yaml"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
