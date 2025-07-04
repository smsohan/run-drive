package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestFileHandler tests the main HTTP handler for serving files and listing directories.
func TestFileHandler(t *testing.T) {
	// Create a temporary directory for testing.
	tmpDir, err := os.MkdirTemp("", "test-downloads")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override the downloadDir for the duration of the test.
	originalDownloadDir := downloadDir
	downloadDir = tmpDir
	defer func() { downloadDir = originalDownloadDir }()

	// Create some dummy files and directories for testing.
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755); err != nil {
		t.Fatalf("Failed to create test subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("world"), 0644); err != nil {
		t.Fatalf("Failed to create test file in subdir: %v", err)
	}

	// --- Test Cases ---
	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
		isJSON         bool
	}{
		{
			name:           "List root directory",
			path:           "/",
			expectedStatus: http.StatusOK,
			expectedBody:   `["file1.txt","subdir/"]`,
			isJSON:         true,
		},
		{
			name:           "List subdirectory",
			path:           "/subdir/",
			expectedStatus: http.StatusOK,
			expectedBody:   `["file2.txt"]`,
			isJSON:         true,
		},
		{
			name:           "Get file in root",
			path:           "/file1.txt",
			expectedStatus: http.StatusOK,
			expectedBody:   "hello",
		},
		{
			name:           "Get file in subdirectory",
			path:           "/subdir/file2.txt",
			expectedStatus: http.StatusOK,
			expectedBody:   "world",
		},
		{
			name:           "File not found",
			path:           "/nonexistent.txt",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Directory traversal attempt",
			path:           "/../..",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(fileHandler)
			handler.ServeHTTP(rr, req)

			if status := rr.Code; status != tt.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v", status, tt.expectedStatus)
			}

			if tt.expectedBody != "" {
				if tt.isJSON {
					// For JSON, we need to unmarshal and compare to avoid issues with whitespace/ordering.
					var actual []string
					if err := json.Unmarshal(rr.Body.Bytes(), &actual); err != nil {
						t.Fatalf("Failed to unmarshal JSON response: %v", err)
					}
					var expected []string
					if err := json.Unmarshal([]byte(tt.expectedBody), &expected); err != nil {
						t.Fatalf("Failed to unmarshal expected JSON: %v", err)
					}
					if len(actual) != len(expected) {
						t.Errorf("handler returned unexpected body: got %v want %v", actual, expected)
					}
				} else {
					if rr.Body.String() != tt.expectedBody {
						t.Errorf("handler returned unexpected body: got %v want %v", rr.Body.String(), tt.expectedBody)
					}
				}
			}
		})
	}
}
