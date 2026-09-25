FROM golang:1.25.11-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The runtime image is Alpine (musl), so always build a self-contained binary.
# This avoids a host-side `go build` producing a glibc-linked executable that
# Alpine reports as "no such file or directory" at startup.
RUN CGO_ENABLED=0 go build -o /out/bot .

FROM alpine:latest

WORKDIR /app

# 1. Install base packages and set timezone (100% cached)
RUN apk add --no-cache ca-certificates tzdata util-linux bash python3 py3-pillow font-noto-cjk
ENV TZ=Asia/Seoul

# 2. Large static fonts/assets (~80MB, 100% cached across bot code changes)
COPY assets ./assets

# 3. Copy the self-contained binary built above.
COPY --from=builder /out/bot ./bot

# Run the bot
ENTRYPOINT ["./bot"]
