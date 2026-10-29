# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /app

# gcc and musl-dev: LMDB needs CGo
RUN apk add --no-cache git ca-certificates gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o relay ./cmd/relay

FROM alpine:latest

RUN addgroup -S relay && adduser -S relay -G relay

WORKDIR /app

COPY --from=builder /app/relay /app/relay

RUN mkdir -p /app/data/lmdb && chown -R relay:relay /app/data

EXPOSE 3334

USER relay

VOLUME ["/app/data"]

ENTRYPOINT ["/app/relay"]
