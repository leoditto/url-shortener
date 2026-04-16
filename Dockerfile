# Build Stage
FROM golang:1.25-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum to download dependencies
COPY go.mod .
COPY go.sum .

# Download Go modules
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o url-shortener .

# Run Stage
FROM alpine:latest

# Install curl for health checks (required by docker-compose healthcheck)
RUN apk add --no-cache curl

# Set the working directory inside the container
WORKDIR /app

# Copy the compiled binary from the build stage
COPY --from=builder /app/url-shortener .

# Expose the port the application listens on
EXPOSE 8080

# Command to run the application
CMD ["./url-shortener"]