# Build stage
FROM golang:1.25-alpine AS builder

# Build arguments for multi-architecture support
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application with optimizations for the target architecture
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o argazer .

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests and timezone data
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 -S argazer && \
    adduser -u 1001 -S argazer -G argazer

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /build/argazer .

# Copy example configs
COPY --from=builder /build/examples/ ./examples/

# Change ownership to non-root user
RUN chown -R argazer:argazer /app

# Switch to non-root user
USER argazer

# Run the application
# Config can be mounted at /app/config.yaml or passed via environment variables
ENTRYPOINT ["./argazer"]
CMD ["--config", "/app/config.yaml"]

