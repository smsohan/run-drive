# --- Stage 1: Build ---
# Use an official Go image to build the application.
# Using alpine for a smaller build image.
FROM golang:1.22-alpine AS builder

# Set the working directory inside the container.
WORKDIR /app

# Copy the Go module files and download dependencies.
# This is done in a separate layer to leverage Docker's layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code into the container.
COPY *.go ./

# Build the Go application, creating a statically linked binary.
# CGO_ENABLED=0 is important for creating a static binary that can run in a minimal container.
# -ldflags="-w -s" strips debugging information, reducing the binary size.
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-w -s" -o /gdrive-sync .

# --- Stage 2: Runtime ---
# Use a minimal base image for the final container.
# Alpine is a good choice for its small size.
FROM alpine:latest

# The ca-certificates package is necessary for making HTTPS requests to Google APIs.
RUN apk --no-cache add ca-certificates

# Set the working directory.
WORKDIR /root/

# Copy the built binary from the 'builder' stage.
COPY --from=builder /gdrive-sync .

# Set the binary as the entrypoint for the container.
ENTRYPOINT ["./gdrive-sync"]

# Set a default command. The user can override this at runtime.
# For example: docker run <image_name> --folder-name="My-Folder"
CMD ["--folder-name=agents"]
