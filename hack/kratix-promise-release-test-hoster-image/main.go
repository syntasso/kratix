package main

import (
	"fmt"
	"log"
	"os"

	gohttp "net/http"

	"encoding/base64"

	"github.com/gorilla/mux"
)

func main() {
	router := mux.NewRouter()
	router.HandleFunc("/promise/{name}", func(w gohttp.ResponseWriter, r *gohttp.Request) {
		vars := mux.Vars(r)
		name := vars["name"]
		fmt.Println("fetching promise: " + name)
		promiseContent, err := base64.StdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			panic(err)
		}
		w.Write([]byte(promiseContent))
	}).Methods("GET")

	srv := &gohttp.Server{
		Addr:    ":8080",
		Handler: router,
	}

	if err := srv.ListenAndServe(); err != nil && err != gohttp.ErrServerClosed {
		log.Fatalf("listen: %s\n", err)
	}
}
