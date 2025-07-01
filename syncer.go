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

// performSync handles one complete sync cycle.
func performSync(ctx context.Context, driveService *drive.Service, folderName string, since time.Time) (time.Time, error) {
	currentTime := time.Now()
	var queryParts []string

	folderID, err := getFolderID(ctx, driveService, folderName)
	if err != nil {
		return currentTime, fmt.Errorf("error finding folder: %w", err)
	}
	queryParts = append(queryParts, fmt.Sprintf("'%s' in parents", folderID))

	if !since.IsZero() {
		rfc3339Timestamp := since.Format(time.RFC3339)
		queryParts = append(queryParts, fmt.Sprintf("modifiedTime > '%s'", rfc3339Timestamp))
	}

	query := strings.Join(queryParts, " and ")

	r, err := driveService.Files.List().
		Context(ctx).
		Q(query).
		PageSize(100).
		Fields("files(id, name, modifiedTime, sha256Checksum)").
		OrderBy("modifiedTime desc").
		Do()
	if err != nil {
		return currentTime, fmt.Errorf("unable to retrieve files: %w", err)
	}

	if len(r.Files) == 0 {
		fmt.Println("No new or updated files found.")
		return currentTime, nil
	}

	fmt.Printf("Found %d file(s), checking for updates...\n", len(r.Files))
	for _, file := range r.Files {
		downloadFile(driveService, file, downloadDir)
	}
	return currentTime, nil
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

// downloadFile downloads a file from Drive if it doesn't exist locally or if the checksums differ.
func downloadFile(srv *drive.Service, file *drive.File, dir string) {
	localPath := filepath.Join(dir, file.Name)

	if _, err := os.Stat(localPath); err == nil {
		localSHA256, err := calculateLocalSHA256(localPath)
		if err != nil {
			log.Printf("Could not calculate SHA256 for %s: %v. Re-downloading...", file.Name, err)
		} else if localSHA256 == file.Sha256Checksum {
			fmt.Printf("File '%s' is already up to date. Skipping.\n", file.Name)
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
		return
	}

	fmt.Printf("Successfully downloaded and saved '%s' to %s\n", file.Name, localPath)
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
