# Build stage
FROM golang:alpine AS builder

WORKDIR /app

# Install system dependencies
RUN apk add --no-cache git

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source files
COPY *.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o bot .

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install CA certificates and util-linux for nsenter host command execution
RUN apk add --no-cache ca-certificates util-linux bash

# Copy binary from builder
COPY --from=builder /app/bot ./bot

# Run the bot
CMD ["./bot"]
