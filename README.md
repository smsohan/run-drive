# Google Drive to Local Sync Service

This project is a Go application that continuously synchronizes a specified folder from Google Drive to a local directory. It runs a background service to poll for changes and an HTTP server to serve the downloaded files.

## Features

- **Continuous Synchronization**: Polls a Google Drive folder every 30 seconds to check for new or updated files.
- **Recursive Sync**: Mirrors the entire folder structure from the target Google Drive folder to the local file system.
- **Efficient Downloads**: Uses SHA256 checksums to avoid re-downloading files that haven't changed.
- **Google Docs Export**: Automatically exports Google Workspace files (Docs, Sheets, etc.) as PDFs, as they cannot be downloaded directly.
- **HTTP File Server**: Runs an HTTP server on port `8080` to serve the synchronized files and list directory contents.
- **Graceful Shutdown**: Handles `SIGINT` and `SIGTERM` signals to shut down cleanly.
- **Cloud Native**: Designed to run locally, in a Docker container, or deployed as a serverless service on Google Cloud Run.

## How It Works

The application consists of two main components running as concurrent goroutines:

1.  **Background Syncer**: A continuous loop that polls a specific Google Drive folder. It recursively scans for changes since the last sync and downloads any new or modified files to the local `/tmp/agents-state` directory.
2.  **HTTP Server**: A web server that listens for `GET` requests. It can serve the content of specific files or provide a JSON listing of a directory's contents.

Authentication is handled using **Application Default Credentials (ADC)**. The application automatically finds and uses credentials from the environment, making it seamless to switch between local development (using a service account key file) and a Google Cloud environment (using an attached service account).

## Setup and Configuration

### 1. Google Drive & Service Account Setup

To allow the application to access a folder, you must share that folder with a Google Cloud Service Account.

**Step 1: Create a Service Account**
1.  Go to the [Service Accounts page](https://console.cloud.google.com/iam-admin/serviceaccounts) in the Google Cloud Console.
2.  Select your project and click **"Create Service Account"**.
3.  Give it a name (e.g., `drive-sync-service`) and click **"Create and Continue"**.
4.  You can skip granting project-level roles for now. Click **"Done"**.
5.  After the service account is created, copy its email address (e.g., `drive-sync-service@<your-project-id>.iam.gserviceaccount.com`).

**Step 2: Share the Google Drive Folder**
1.  Go to Google Drive and find the folder you want to sync.
2.  Right-click the folder and select **"Share"**.
3.  Paste the service account's email address into the sharing dialog.
4.  Grant it **"Viewer"** access.
5.  Click **"Share"**.


You can then interact with the server using `curl`:

```bash
# List files in the root of the download directory
curl http://<host>:<port>/

# Request the content of a specific file
curl http://<host>:<port>/file-name.txt

# or list the contents of a directory
curl http://<host>:<port>/dir-name
```

### Building and Running with Docker

A multi-stage `Dockerfile` is provided to create a minimal, secure runtime image.

## Deployment to Google Cloud Run

Deploying to Cloud Run is the ideal way to run this service, as it handles authentication automatically without needing key files.

**2. Deploy to Cloud Run:**
When deploying, you associate the service with the same service account you created earlier. Cloud Run uses the attached service account for ADC, so no key file is needed.

```bash
# Deploy the service to Cloud Run
gcloud run deploy ${IMAGE_NAME} \
  --source . \
  --region=${REGION} \
  --allow-unauthenticated \
  --service-account="drive-sync-service@${PROJECT_ID}.iam.gserviceaccount.com" \
  --args='--folder-name=Your-Drive-Folder-Name'
```
The service will now be running in the cloud, securely authenticating with the attached service account.

## Command-Line Flags

-   `--folder-name` (string, **required**): The name of the Google Drive folder to sync.
-   `--seconds-ago` (int, optional, default: `0`): For the *initial* sync, only download files modified in the last N seconds. If set to `0`, all files are synced on the first run. Subsequent syncs always check for changes since the last cycle.

## API Endpoints

-   `GET /`: Returns a JSON array of file and directory names in the root of the download directory (`/tmp/agents-state`).
-   `GET /<path>`:
    -   If `<path>` is a file, serves the raw content of the file.
    -   If `<path>` is a directory, returns a JSON array of its contents.
    -   Path traversal (`../`) is blocked for security.

```bash
# Given the Drive Folder structure
# |- a
# |- c
# |- Gagi
#   |- b
#   |- new-a

# Root URL returns the contents of the synced directory
$ gcurl https://<>.run.app/
["Gagi/","a","c"]

# Pass the name of a directory to see it's contents
$ gcurl https://run-drive-1000276527499.us-east1.run.app/Gagi
["b","new-a"]

# Pass the name of a file to see it's contents
$ gcurl https://run-drive-1000276527499.us-east1.run.app/Gagi/new-a
a
```