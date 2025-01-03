package main

import (
	"log"

	"github.com/syntasso/kratix/work-creator/pipeline/lib"
)

func main() {
	r := lib.Reader{}
	if err := r.Run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
