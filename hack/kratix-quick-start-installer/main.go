package main

import (
	"context"
	"log"

	"github.com/syntasso/kratix/hack/quick-start-installer/lib"
)

func main() {
	ctx := context.Background()

	if err := lib.InstallCertManager(ctx, 1, 4); err != nil {
		log.Fatal(err)
	}

	if err := lib.InstallKratix(ctx, 2, 4); err != nil {
		log.Fatal(err)
	}

	if err := lib.ConfigureKratix(ctx, 3, 4); err != nil {
		log.Fatal(err)
	}

	lib.FinalizeInstall(4)
}
