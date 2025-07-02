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

// performSync starts the synchronization process.
func performSync(ctx context.Context, driveService *drive.Service, folderName string, since time.Time) (time.Time, error) {
	currentTime := time.Now()

	folderID, err := getFolderID(ctx, driveService, folderName)
	if err != nil {
		return currentTime, fmt.Errorf("error finding folder: %w", err)
	}

	fmt.Printf("Starting recursive sync for folder '%s'...\n", folderName)
	err = syncFolderRecursively(ctx, driveService, folderID, downloadDir, since)
	if err != nil {
		return currentTime, fmt.Errorf("recursive sync failed: %w", err)
	}

	return currentTime, nil
}

// syncFolderRecursively traverses a folder and its sub-folders to sync files.
func syncFolderRecursively(ctx context.Context, srv *drive.Service, folderID, localPath string, since time.Time) error {
	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	err := srv.Files.List().
		Context(ctx).
		Q(query).
		Fields("files(id, name, mimeType, createdTime, sha256Checksum)").
		Pages(ctx, func(page *drive.FileList) error {
			for _, file := range page.Files {
				fmt.Printf("Processing file: %s, created: %s\n", file.Name, file.CreatedTime)

				newLocalPath := filepath.Join(localPath, file.Name)

				if file.MimeType == "application/vnd.google-apps.folder" {
					fmt.Printf("Entering directory: %s\n", newLocalPath)
					if err := os.MkdirAll(newLocalPath, 0755); err != nil {
						log.Printf("Failed to create directory %s: %v", newLocalPath, err)
						continue
					}
					// Recursively sync the sub-folder.
					if err := syncFolderRecursively(ctx, srv, file.Id, newLocalPath, since); err != nil {
						log.Printf("Failed to sync sub-folder %s: %v", file.Name, err)
					}
				} else {
					// It's a file; check if it was created since the last sync.
					createTime, err := time.Parse(time.RFC3339, file.CreatedTime)
					if err != nil {
						log.Printf("Could not parse created time for %s: %v", file.Name, err)
						continue
					}

					if since.IsZero() || createTime.After(since) {
						downloadFile(srv, file, localPath) // Pass the parent directory path.
					}
				}
			}
			return nil
		})

	if err != nil {
		return fmt.Errorf("could not list files for folder ID %s: %w", folderID, err)
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

// downloadFile handles downloading/exporting a file from Drive.
func downloadFile(srv *drive.Service, file *drive.File, dir string) {
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
		exportGoogleDoc(srv, file, dir)
		return
	}
	downloadBinaryFile(srv, file, dir)
}

// exportGoogleDoc exports a Google Workspace file as a PDF.
func exportGoogleDoc(srv *drive.Service, file *drive.File, dir string) {
	localPath := filepath.Join(dir, file.Name+".pdf")
	fmt.Printf("Exporting Google Doc '%s' to %s\n", file.Name, localPath)

	resp, err := srv.Files.Export(file.Id, "application/pdf").Download()
	if err != nil {
		log.Printf("Error exporting file %s: %v", file.Name, err)
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

// downloadBinaryFile downloads a regular file and checks its SHA256.
func downloadBinaryFile(srv *drive.Service, file *drive.File, dir string) {
	localPath := filepath.Join(dir, file.Name)

	if _, err := os.Stat(localPath); err == nil {
		localSHA256, err := calculateLocalSHA256(localPath)
		if err != nil {
			log.Printf("Could not calculate SHA256 for %s: %v. Re-downloading...", file.Name, err)
		} else if localSHA256 == file.Sha256Checksum {
			// File is already up to date.
			return
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
