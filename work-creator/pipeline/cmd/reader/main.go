package main

import (
	"context"
	"log"
	"os"

	"github.com/syntasso/kratix/work-creator/pipeline/lib"
)

func main() {
	ctx := context.Background()
	r := lib.Reader{
		Out: os.Stdout,
	}
	if err := r.Run(ctx); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
