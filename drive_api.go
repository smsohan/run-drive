package main

import (
	"context"
	"fmt"
	"io"

	"google.golang.org/api/drive/v3"
)

// DriveAPI defines the interface for the Google Drive operations used by the syncer.
// This allows for a mock implementation to be used during testing.
type DriveAPI interface {
	ListFiles(ctx context.Context, query string) ([]*drive.File, error)
	DownloadFile(fileID string) (io.ReadCloser, error)
	GetFolderID(ctx context.Context, name string) (string, error)
}

// driveService implements the DriveAPI interface using the real Google Drive service.
type driveService struct {
	srv *drive.Service
}

// NewDriveService creates a new wrapper for the real drive service.
func NewDriveService(srv *drive.Service) DriveAPI {
	return &driveService{srv: srv}
}

func (ds *driveService) ListFiles(ctx context.Context, query string) ([]*drive.File, error) {
	var files []*drive.File
	err := ds.srv.Files.List().
		Context(ctx).
		Q(query).
		Fields("files(id, name, mimeType, modifiedTime, sha256Checksum)").
		Pages(ctx, func(page *drive.FileList) error {
			files = append(files, page.Files...)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (ds *driveService) DownloadFile(fileID string) (io.ReadCloser, error) {
	resp, err := ds.srv.Files.Get(fileID).Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (ds *driveService) GetFolderID(ctx context.Context, name string) (string, error) {
	query := fmt.Sprintf("mimeType='application/vnd.google-apps.folder' and name='%s'", name)
	files, err := ds.ListFiles(ctx, query)
	if err != nil {
		return "", fmt.Errorf("unable to retrieve folder: %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("folder '%s' not found", name)
	}
	return files[0].Id, nil
}
