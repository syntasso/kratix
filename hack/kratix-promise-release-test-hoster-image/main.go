package main

import (
	"fmt"
	"log"
	"os"

	"net/http"

	"encoding/base64"

	"github.com/gorilla/mux"
)

func SecureHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]
	fmt.Println("fetching promise: " + name)

	authHeader := r.Header.Get("Authorization")
	fmt.Println("Checking Authorization header:" + authHeader)
	if authHeader == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if authHeader != "Bearer your-secret-token" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	promiseContent, err := base64.StdEncoding.DecodeString(os.Getenv(name))
	if err != nil {
		panic(err)
	}
	w.Write(promiseContent)
}

func main() {
	router := mux.NewRouter()
	router.HandleFunc("/promise/{name}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		name := vars["name"]
		fmt.Println("fetching promise: " + name)
		promiseContent, err := base64.StdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			panic(err)
		}
		w.Write(promiseContent)
	}).Methods("GET")
	router.HandleFunc("/secure/promise/{name}", SecureHandler).Methods("GET")

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %s\n", err)
	}
}
