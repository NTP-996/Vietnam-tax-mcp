# The tax-law MCP server: one static binary in a distroless image, with the
# rule packs at /rules and the legal corpus at /legal. Build from the
# repository root:
#
#   docker build --build-arg GO_IMAGE=<versions.env GO_IMAGE> -t vietnam-tax-mcp .
#
# GO_IMAGE has no default on purpose: versions.env is the single source of the pin.

ARG GO_IMAGE
FROM ${GO_IMAGE} AS build
# The image tag, reported by the MCP server version and /healthz.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/mcp ./cmd/mcp

# distroless: no shell, no package manager; `mcp -healthcheck` probes the process itself.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp /mcp
COPY rules/ /rules/
COPY legal/manifest.yaml /legal/manifest.yaml
COPY legal/corpus/ /legal/corpus/
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/mcp"]
