package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"fakeproject/internal/auth"
	"fakeproject/internal/config"
	"fakeproject/internal/db"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "ok\n")
}

func main() {
	cfg := config.DefaultConfig()

	dbConn, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer dbConn.Close()

	if err := dbConn.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	http.HandleFunc("/health", healthHandler)

	http.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		user, err := auth.ValidateToken(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, "user: %s\n", user.Name)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("starting on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
