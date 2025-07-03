package main

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
)

// mockDriveAPI is a mock implementation of the DriveAPI interface for testing.
type mockDriveAPI struct {
	files       map[string][]*drive.File
	fileContent map[string]string
	folders     map[string]string
}

func (m *mockDriveAPI) ListFiles(ctx context.Context, query string) ([]*drive.File, error) {
	// A simple mock: extract the parent ID from the query to return the correct file list.
	parts := strings.Split(query, " ")
	parentID := strings.Trim(parts[0], "'")
	return m.files[parentID], nil
}

func (m *mockDriveAPI) GetFolderID(ctx context.Context, name string) (string, error) {
	// A simple mock: extract the parent ID from the query to return the correct file list.
	return m.folders[name], nil
}

func (m *mockDriveAPI) DownloadFile(fileID string) (io.ReadCloser, error) {
	content, ok := m.fileContent[fileID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return ioutil.NopCloser(strings.NewReader(content)), nil
}

// TestPerformSync tests the main synchronization logic.
func TestPerformSync(t *testing.T) {
	// --- Setup ---
	tmpDir, err := os.MkdirTemp("", "test-sync")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override downloadDir for the test.
	originalDownloadDir := downloadDir
	downloadDir = tmpDir
	defer func() { downloadDir = originalDownloadDir }()

	// Create a mock Drive API with some test data.
	mockAPI := &mockDriveAPI{
		files: map[string][]*drive.File{
			"root_folder_id": {
				{Id: "file1_id", Name: "file1.txt", MimeType: "text/plain", Sha256Checksum: "sha_file1"},
				{Id: "subfolder_id", Name: "subfolder", MimeType: "application/vnd.google-apps.folder"},
			},
			"subfolder_id": {
				{Id: "file2_id", Name: "file2.txt", MimeType: "text/plain", Sha256Checksum: "sha_file2"},
			},
		},
		folders: map[string]string{
			"test-folder": "root_folder_id",
			"subfolder":   "subfolder_id",
		},
		fileContent: map[string]string{
			"file1_id": "content1",
			"file2_id": "content2",
		},
	}

	// --- Test Execution ---
	shaCache := make(map[string]string)
	_, err = performSync(context.Background(), mockAPI, "test-folder", time.Time{}, shaCache)
	if err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	// --- Assertions ---
	// Check if files were downloaded correctly.
	assertFileContent(t, filepath.Join(tmpDir, "file1.txt"), "content1")
	assertFileContent(t, filepath.Join(tmpDir, "subfolder", "file2.txt"), "content2")

	// Check if the SHA cache was populated.
	if shaCache[filepath.Join(tmpDir, "file1.txt")] != "sha_file1" {
		t.Errorf("SHA cache not populated for file1.txt")
	}

	// --- Test Pruning ---
	// Remove a file from the mock remote and re-run sync.
	mockAPI.files["root_folder_id"] = []*drive.File{
		{Id: "subfolder_id", Name: "subfolder", MimeType: "application/vnd.google-apps.folder"},
	}
	_, err = performSync(context.Background(), mockAPI, "test-folder", time.Now(), shaCache)
	if err != nil {
		t.Fatalf("second performSync failed: %v", err)
	}

	// Check if the local file was pruned.
	if _, err := os.Stat(filepath.Join(tmpDir, "file1.txt")); !os.IsNotExist(err) {
		t.Errorf("file1.txt was not pruned after being deleted from remote")
	}
}

// assertFileContent is a helper to check the content of a file.
func assertFileContent(t *testing.T, path, expectedContent string) {
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read file %s: %v", path, err)
	}
	if string(content) != expectedContent {
		t.Errorf("File content for %s is wrong: got %q, want %q", path, string(content), expectedContent)
	}
}
