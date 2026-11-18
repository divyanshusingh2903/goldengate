# Golden Gate Bridge server image. Build from the repository root:
#   docker build -f deploy/Dockerfile --build-arg VERSION=dev -t goldengatebridge .
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/bridgecli.Version=${VERSION}" \
    -o /out/goldengatebridge ./cmd/goldengatebridge

FROM gcr.io/distroless/static-debian12
ARG VERSION=dev
LABEL org.opencontainers.image.title="goldengatebridge" \
      org.opencontainers.image.description="Golden Gate Bridge: stores shared work summaries and serves them read-only over MCP" \
      org.opencontainers.image.source="https://github.com/divyanshusingh2903/goldengate" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/goldengatebridge /goldengatebridge
# Hosts such as Railway and Cloud Run set PORT themselves; this is the default elsewhere.
ENV PORT=8080
# Mount a volume here: the database is the only state. Runs as root so a freshly mounted volume is writable.
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/goldengatebridge"]
CMD ["serve", "--behind-tls-proxy", "--db", "/data/bridge.db"]
