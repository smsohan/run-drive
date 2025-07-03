package main

import (
	"context"
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

var downloadDir = "/tmp/agents-state"

const syncInterval = 30 * time.Second

// startSyncLoop runs the file synchronization process in a continuous loop.
func startSyncLoop(ctx context.Context, folderName string, secondsAgo int) {
	var driveAPI DriveAPI

	log.Println("Authenticating using Application Default Credentials.")
	client, err := google.DefaultClient(ctx, drive.DriveReadonlyScope)
	if err != nil {
		log.Fatalf("Unable to create Google Drive client with ADC: %v", err)
	}
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to create Drive service with ADC: %v", err)
	}
	driveAPI = NewDriveService(driveService)

	var lastSyncTime time.Time
	if secondsAgo > 0 {
		lastSyncTime = time.Now().Add(-time.Duration(secondsAgo) * time.Second)
	}

	shaCache := make(map[string]string)

	for {
		select {
		case <-ctx.Done():
			log.Println("Sync loop shutting down.")
			return
		default:
			fmt.Printf("\nStarting sync cycle...\n")
			newSyncTime, err := performSync(ctx, driveAPI, folderName, lastSyncTime, shaCache)
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
func performSync(ctx context.Context, driveAPI DriveAPI, folderName string, since time.Time, shaCache map[string]string) (time.Time, error) {
	currentTime := time.Now()

	rootFolderID, err := driveAPI.GetFolderID(ctx, folderName)
	if err != nil {
		return currentTime, fmt.Errorf("error finding folder: %w", err)
	}

	remotePaths := make(map[string]bool)
	remotePaths[downloadDir] = true

	fmt.Printf("Starting recursive sync for folder '%s'...\n", folderName)
	err = syncFolderRecursively(ctx, driveAPI, rootFolderID, downloadDir, since, remotePaths, shaCache)
	if err != nil {
		return currentTime, fmt.Errorf("recursive sync failed: %w", err)
	}

	fmt.Println("Sync complete. Pruning local files that were deleted on Drive...")
	err = pruneLocalFiles(downloadDir, remotePaths, shaCache)
	if err != nil {
		return currentTime, fmt.Errorf("failed to prune local files: %w", err)
	}

	return currentTime, nil
}

func syncFolderRecursively(ctx context.Context, driveAPI DriveAPI, folderID, localPath string, since time.Time, remotePaths map[string]bool, shaCache map[string]string) error {
	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	files, err := driveAPI.ListFiles(ctx, query)
	if err != nil {
		return err
	}

	for _, file := range files {
		newLocalPath := filepath.Join(localPath, file.Name)

		if file.MimeType == "application/vnd.google-apps.folder" {
			remotePaths[newLocalPath] = true
			if err := os.MkdirAll(newLocalPath, 0755); err != nil {
				log.Printf("Failed to create directory %s: %v", newLocalPath, err)
				continue
			}
			if err := syncFolderRecursively(ctx, driveAPI, file.Id, newLocalPath, since, remotePaths, shaCache); err != nil {
				log.Printf("Failed to sync sub-folder %s: %v", file.Name, err)
			}
		} else if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
			log.Printf("Skipping Google Workspace file: %s", file.Name)
			continue
		} else {
			remotePaths[newLocalPath] = true
			downloadFile(driveAPI, file, localPath, shaCache)
		}
	}
	return nil
}

func downloadFile(driveAPI DriveAPI, file *drive.File, dir string, shaCache map[string]string) {
	localPath := filepath.Join(dir, file.Name)

	if _, err := os.Stat(localPath); err == nil {
		localSHA256, found := shaCache[localPath]
		if found && localSHA256 == file.Sha256Checksum {
			return
		}
		fmt.Printf("File '%s' has changed. Downloading new version.\n", file.Name)
	} else {
		fmt.Printf("File '%s' not found locally. Downloading.\n", file.Name)
	}

	body, err := driveAPI.DownloadFile(file.Id)
	if err != nil {
		log.Printf("Error downloading %s: %v", file.Name, err)
		return
	}
	defer body.Close()

	outFile, err := os.Create(localPath)
	if err != nil {
		log.Printf("Error creating file %s: %v", localPath, err)
		return
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, body); err != nil {
		log.Printf("Error writing to file %s: %v", localPath, err)
		delete(shaCache, localPath)
		return
	}

	shaCache[localPath] = file.Sha256Checksum
}

func pruneLocalFiles(localRoot string, remotePaths map[string]bool, shaCache map[string]string) error {
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

	sort.Slice(pathsToDelete, func(i, j int) bool {
		return len(pathsToDelete[i]) > len(pathsToDelete[j])
	})

	for _, path := range pathsToDelete {
		fmt.Printf("Pruning deleted item: %s\n", path)
		if err := os.RemoveAll(path); err != nil {
			log.Printf("Failed to prune path %s: %v", path, err)
		} else {
			delete(shaCache, path)
		}
	}
	return nil
}
