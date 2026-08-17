# Build stage
FROM --platform=$BUILDPLATFORM golang:1.26.6-trixie AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    build-essential \
    libc6-dev \
    gcc-aarch64-linux-gnu \
    libc6-dev-arm64-cross \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "arm64" ]; then \
    CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o html-bundler . ; \
    else \
    CGO_ENABLED=1 CC=gcc GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o html-bundler . ; \
    fi

# Final stage
FROM debian:stable-slim

WORKDIR /root

# Copy the binary from builder
COPY --from=builder /app/html-bundler .
COPY --from=builder /app/zstd_dict_v2 ./zstd_dict_v2

# Expose the default port
EXPOSE 8080

# Run the application
ENTRYPOINT ["./html-bundler"]