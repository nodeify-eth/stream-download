FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/stream-download ./cmd/stream-download

FROM debian:bookworm-slim
LABEL org.opencontainers.image.source="https://github.com/nodeify-eth/stream-download"
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates aria2 zstd lz4 xz-utils \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stream-download /usr/local/bin/stream-download
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/stream-download"]
