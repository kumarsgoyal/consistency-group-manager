# Build stage
FROM docker.io/library/golang:1.22.7 AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files (go.mod and go.sum)
COPY go.mod go.sum ./

# Download dependencies
RUN go mod tidy

# Copy the source code into the container
COPY . .

# Build the application binary with static linking
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -a -race -o log-replica main.go

# Runtime stage
FROM alpine:latest


# Set the working directory inside the container
WORKDIR /app

# local
# Install minimal runtime dependencies for binaries with CGO
RUN apk add --no-cache libc6-compat

# Copy the compiled binary from the build stage
COPY --from=builder /app/log-replica /app/

RUN mkdir -p /app/data

# Expose the required port
EXPOSE 8080

# Set the entrypoint for the container to run the app
CMD ["/app/log-replica"]
