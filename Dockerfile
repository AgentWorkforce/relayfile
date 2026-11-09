# syntax=docker/dockerfile:1

ARG GO_IMAGE=golang:1.22-alpine
ARG RUNTIME_IMAGE=alpine:3.20

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/relayfile ./cmd/relayfile && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/relayfile-mount ./cmd/relayfile-mount

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates curl bash

COPY --from=build /out/relayfile /usr/local/bin/relayfile
COPY --from=build /out/relayfile-mount /usr/local/bin/relayfile-mount

EXPOSE 8080
CMD ["/usr/local/bin/relayfile"]
