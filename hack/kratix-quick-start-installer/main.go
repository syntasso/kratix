package main

import (
	"context"
	"log"

	"github.com/syntasso/quick-start-installer/lib"
)

func main() {
	ctx := context.Background()

	if err := lib.InstallCertManager(ctx); err != nil {
		log.Fatal(err)
	}

	if err := lib.InstallKratix(ctx); err != nil {
		log.Fatal(err)
	}

	if err := lib.ConfigureKratix(ctx); err != nil {
		log.Fatal(err)
	}

	lib.FinalizeInstall()
}
