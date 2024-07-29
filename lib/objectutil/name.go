package objectutil

import (
	"regexp"

	"github.com/syntasso/kratix/lib/hash"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const maxNameLength = 63 - 1 - 5 //five char sha, plus the separator

func GenerateObjectName(name string) string {
	name = processName(name)

	id := uuid.NewUUID()

	return name + "-" + string(id[0:5])
}

func GenerateDeterministicObjectName(name string) string {
	name = processName(name)

	id := hash.ComputeHash(name)

	return name + "-" + string(id[0:5])
}

func isAlphanumeric(s byte) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9]*$`).MatchString(string(s))
}

func processName(name string) string {
	if len(name) > maxNameLength {
		name = name[0 : maxNameLength-1]
	}

	for !isAlphanumeric(name[len(name)-1]) {
		name = name[0 : len(name)-1]
	}

	return name
}
