# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/andydixon/noema/internal/version.Version=${VERSION}" -o /out/ ./cmd/...

FROM alpine:3.20
RUN apk add --no-cache ca-certificates git tzdata && adduser -D -u 10001 noema
COPY --from=build /out/noemad /out/noemactl /usr/local/bin/
USER noema
ENV NOEMA_HTTP_ADDR=0.0.0.0:8080 NOEMA_RESEARCH_DIR=/research
EXPOSE 8080
ENTRYPOINT ["noemad"]
