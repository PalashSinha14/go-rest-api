package main

import (
	"fmt"
	"net/http"
)

func main() {
	// Define a route "/"
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello! Your Go server is running 🎉")
	})

	// Start the server on port 8080
	fmt.Println("Server started on http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}
