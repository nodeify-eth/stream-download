ARG VERSION=dev
FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS build
ARG VERSION
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X main.toolVersion=${VERSION}" -o /out/stream-download ./cmd/stream-download

FROM debian:bookworm-slim@sha256:96e378d7e6531ac9a15ad505478fcc2e69f371b10f5cdf87857c4b8188404716
LABEL org.opencontainers.image.source="https://github.com/nodeify-eth/stream-download"
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    ca-certificates=20230311+deb12u1 \
    zstd=1.5.4+dfsg2-5 \
    lz4=1.9.4-1 \
    xz-utils=5.4.1-1 \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stream-download /usr/local/bin/stream-download
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/stream-download"]
