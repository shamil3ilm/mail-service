# syntax=docker/dockerfile:1.7

# ─── Stage 1: build ────────────────────────────────────────────
FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/mailservice ./cmd/mailservice

# ─── Stage 2: runtime ──────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S mail && adduser -S -G mail mail

WORKDIR /app
COPY --from=build /out/mailservice /app/mailservice

USER mail
EXPOSE 8035 2525 587

ENV MAIL_MODE=local \
    MAIL_LISTEN_ADDR=0.0.0.0 \
    MAIL_LOG_FORMAT=json \
    MAIL_DB_PATH=/data/mail.db \
    MAIL_RAW_STORE_PATH=/data/raw

VOLUME ["/data"]

ENTRYPOINT ["/app/mailservice"]
