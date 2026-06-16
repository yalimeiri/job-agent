# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
FROM golang:1.22-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git ca-certificates

# Cache dependencies.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary so it runs on the slim runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/agent ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/test-scraper ./cmd/test-scraper

# ---- runtime stage ---------------------------------------------------------
FROM alpine:3.20

# Chromium for go-rod headless automation, plus certs and fonts.
RUN apk add --no-cache \
        chromium \
        chromium-chromedriver \
        nss freetype harfbuzz ca-certificates ttf-freefont \
        tzdata && \
    addgroup -S agent && adduser -S agent -G agent

ENV ROD_BROWSER_BIN=/usr/bin/chromium-browser

WORKDIR /app
COPY --from=builder /out/agent /app/agent
COPY --from=builder /out/test-scraper /app/test-scraper
COPY migrations /app/migrations
COPY config.example.yaml /app/config.example.yaml

# Screenshots directory writable by the non-root user.
RUN mkdir -p /app/screenshots /app/assets && chown -R agent:agent /app
USER agent

ENTRYPOINT ["/app/agent"]
CMD ["-config", "config.yaml", "-migrations", "/app/migrations"]
