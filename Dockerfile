# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/relayfile ./cmd/relayfile && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/relayfile-mount ./cmd/relayfile-mount

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl bash

COPY --from=build /out/relayfile /usr/local/bin/relayfile
COPY --from=build /out/relayfile-mount /usr/local/bin/relayfile-mount

EXPOSE 8080
CMD ["/usr/local/bin/relayfile"]
