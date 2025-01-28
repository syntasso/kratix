package main

import (
	"fmt"
	"log"
	"os"

	"net/http"

	"github.com/gorilla/mux"
)

func SecureHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("fetching secure promise")

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

	promiseContent, err := os.ReadFile("secure-promise.yaml")
	if err != nil {
		panic(err)
	}
	w.Write(promiseContent)
}

func main() {
	router := mux.NewRouter()
	router.HandleFunc("/promise", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("fetching insecure promise")
		promiseContent, err := os.ReadFile("insecure-promise.yaml")
		if err != nil {
			panic(err)
		}
		if err != nil {
			panic(err)
		}
		w.Write(promiseContent)
	}).Methods("GET")
	router.HandleFunc("/secure/promise", SecureHandler).Methods("GET")

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %s\n", err)
	}
}
