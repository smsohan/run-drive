package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	// Define command-line flags
	folderName := flag.String("folder-name", "agents", "Name of the folder to search within (required)")
	secondsAgo := flag.Int("seconds-ago", 0, "On the first run, list files modified in the last N seconds. If 0, all files are synced.")
	flag.Parse()

	if *folderName == "" {
		log.Fatalf("The --folder-name flag is required.")
	}

	// Set up a context that can be cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up a channel to listen for SIGTERM and SIGINT signals for graceful shutdown.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		fmt.Println()
		log.Printf("Received signal: %s. Shutting down.", sig)
		cancel() // Cancel the context to stop ongoing operations.
		os.Exit(0)
	}()

	// Create the download directory if it doesn't exist
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		log.Fatalf("Failed to create download directory: %v", err)
	}

	// Start the background file syncing process, passing the new key path argument.
	go startSyncLoop(ctx, *folderName, *secondsAgo)

	// Set up and start the HTTP server
	http.HandleFunc("/", fileHandler)
	log.Println("HTTP server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

// fileHandler serves files or lists directory contents based on the request path.
func fileHandler(w http.ResponseWriter, r *http.Request) {
	// Extract the requested path and clean it to prevent traversal attacks.
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	safePath := filepath.Join(downloadDir, requestedPath)

	// --- Security Check ---
	// Ensure the final, cleaned path is still within the intended download directory.
	if !strings.HasPrefix(safePath, downloadDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Check if the path exists.
	info, err := os.Stat(safePath)
	if os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		log.Printf("Error accessing path %s: %v", safePath, err)
		return
	}

	// If the path is a directory, list its contents.
	if info.IsDir() {
		listDirectory(w, safePath)
		return
	}

	// Otherwise, serve the file content.
	http.ServeFile(w, r, safePath)
}

// listDirectory reads a directory and returns a JSON list of its contents.
func listDirectory(w http.ResponseWriter, dirPath string) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		http.Error(w, "Failed to read directory", http.StatusInternalServerError)
		log.Printf("Error reading directory %s: %v", dirPath, err)
		return
	}

	var entries []string
	for _, file := range files {
		entryName := file.Name()
		if file.IsDir() {
			// Add a trailing slash to indicate it's a directory.
			entryName += "/"
		}
		entries = append(entries, entryName)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entries); err != nil {
		log.Printf("Error encoding directory list to JSON: %v", err)
	}
}
