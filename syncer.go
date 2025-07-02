package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const downloadDir = "/tmp/agents-state"
const syncInterval = 30 * time.Second

// startSyncLoop runs the file synchronization process in a continuous loop.
func startSyncLoop(ctx context.Context, folderName string, secondsAgo int) {
	client, err := google.DefaultClient(ctx, drive.DriveReadonlyScope)
	if err != nil {
		log.Fatalf("Unable to create Google Drive client: %v", err)
	}

	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Drive client: %v", err)
	}

	var lastSyncTime time.Time
	if secondsAgo > 0 {
		lastSyncTime = time.Now().Add(-time.Duration(secondsAgo) * time.Second)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("Sync loop shutting down.")
			return
		default:
			fmt.Printf("\nStarting sync cycle...\n")
			newSyncTime, err := performSync(ctx, driveService, folderName, lastSyncTime)
			if err != nil {
				log.Printf("Sync cycle failed: %v. Retrying in %v.", err, syncInterval)
			} else {
				fmt.Printf("Sync cycle complete. Waiting for %v before next sync.\n", syncInterval)
				lastSyncTime = newSyncTime
			}
			time.Sleep(syncInterval)
		}
	}
}

// performSync starts the synchronization process, including pruning of deleted files.
func performSync(ctx context.Context, driveService *drive.Service, folderName string, since time.Time) (time.Time, error) {
	currentTime := time.Now()

	folderID, err := getFolderID(ctx, driveService, folderName)
	if err != nil {
		return currentTime, fmt.Errorf("error finding folder: %w", err)
	}

	// This map will hold all the paths that exist on the remote.
	remotePaths := make(map[string]bool)
	remotePaths[downloadDir] = true // The root directory always exists.

	fmt.Printf("Starting recursive sync for folder '%s'...\n", folderName)
	err = syncFolderRecursively(ctx, driveService, folderID, downloadDir, since, remotePaths)
	if err != nil {
		return currentTime, fmt.Errorf("recursive sync failed: %w", err)
	}

	fmt.Println("Sync complete. Pruning local files that were deleted on Drive...")
	err = pruneLocalFiles(downloadDir, remotePaths)
	if err != nil {
		return currentTime, fmt.Errorf("failed to prune local files: %w", err)
	}

	return currentTime, nil
}

// syncFolderRecursively traverses a folder and its sub-folders to sync files.
func syncFolderRecursively(ctx context.Context, srv *drive.Service, folderID, localPath string, since time.Time, remotePaths map[string]bool) error {
	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	err := srv.Files.List().
		Context(ctx).
		Q(query).
		Fields("files(id, name, mimeType, modifiedTime, sha256Checksum)").
		Pages(ctx, func(page *drive.FileList) error {
			for _, file := range page.Files {
				newLocalPath := filepath.Join(localPath, file.Name)

				if file.MimeType == "application/vnd.google-apps.folder" {
					remotePaths[newLocalPath] = true // Track the directory path.
					if err := os.MkdirAll(newLocalPath, 0755); err != nil {
						log.Printf("Failed to create directory %s: %v", newLocalPath, err)
						continue
					}
					if err := syncFolderRecursively(ctx, srv, file.Id, newLocalPath, since, remotePaths); err != nil {
						log.Printf("Failed to sync sub-folder %s: %v", file.Name, err)
					}
				} else if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
					// This is a Google Workspace file (Doc, Sheet, etc.). Skip it.
					// By not adding it to remotePaths, any old exported .pdf will be pruned.
					log.Printf("Skipping Google Workspace file: %s", file.Name)
					continue
				} else {
					// This is a binary file.
					remotePaths[newLocalPath] = true // Track the file path.
					modTime, err := time.Parse(time.RFC3339, file.ModifiedTime)
					if err != nil {
						log.Printf("Could not parse modified time for %s: %v", file.Name, err)
						continue
					}
					if since.IsZero() || modTime.After(since) {
						downloadFile(srv, file, localPath)
					}
				}
			}
			return nil
		})
	return err
}

// pruneLocalFiles walks the local directory and removes any files or folders not present in the remotePaths map.
func pruneLocalFiles(localRoot string, remotePaths map[string]bool) error {
	var pathsToDelete []string
	err := filepath.Walk(localRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if _, exists := remotePaths[path]; !exists {
			pathsToDelete = append(pathsToDelete, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Sort paths by length in reverse order, so we delete children before parents.
	sort.Slice(pathsToDelete, func(i, j int) bool {
		return len(pathsToDelete[i]) > len(pathsToDelete[j])
	})

	for _, path := range pathsToDelete {
		fmt.Printf("Pruning deleted item: %s\n", path)
		if err := os.Remove(path); err != nil {
			log.Printf("Failed to prune path %s: %v", path, err)
		}
	}
	return nil
}

// getFolderID finds a folder by name and returns its ID.
func getFolderID(ctx context.Context, srv *drive.Service, name string) (string, error) {
	r, err := srv.Files.List().
		Context(ctx).
		Q(fmt.Sprintf("mimeType='application/vnd.google-apps.folder' and name='%s'", name)).
		Fields("files(id)").
		PageSize(1).
		Do()
	if err != nil {
		return "", fmt.Errorf("unable to retrieve folder: %w", err)
	}
	if len(r.Files) == 0 {
		return "", fmt.Errorf("folder '%s' not found", name)
	}
	return r.Files[0].Id, nil
}

// downloadFile downloads a binary file and checks its SHA256 to avoid re-downloading.
func downloadFile(srv *drive.Service, file *drive.File, dir string) {
	localPath := filepath.Join(dir, file.Name)

	if _, err := os.Stat(localPath); err == nil {
		localSHA256, err := calculateLocalSHA256(localPath)
		if err != nil {
			log.Printf("Could not calculate SHA256 for %s: %v. Re-downloading...", file.Name, err)
		} else if localSHA256 == file.Sha256Checksum {
			return // File is already up to date.
		}
		fmt.Printf("File '%s' has changed. Downloading new version.\n", file.Name)
	} else {
		fmt.Printf("File '%s' not found locally. Downloading.\n", file.Name)
	}

	resp, err := srv.Files.Get(file.Id).Download()
	if err != nil {
		log.Printf("Error downloading %s: %v", file.Name, err)
		return
	}
	defer resp.Body.Close()

	outFile, err := os.Create(localPath)
	if err != nil {
		log.Printf("Error creating file %s: %v", localPath, err)
		return
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		log.Printf("Error writing to file %s: %v", localPath, err)
	}
}

// calculateLocalSHA256 computes the SHA256 checksum of a local file.
func calculateLocalSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
