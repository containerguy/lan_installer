# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.5
ARG NODE_VERSION=24.18.0
FROM node:${NODE_VERSION}-alpine3.24 AS web-test
WORKDIR /src
COPY internal/webadmin/assets/catalog.js ./internal/webadmin/assets/catalog.js
COPY internal/webadmin/assets/catalog_cache_helpers.js ./internal/webadmin/assets/catalog_cache_helpers.js
COPY internal/webadmin/assets/catalog_cache_helpers.test.js ./internal/webadmin/assets/catalog_cache_helpers.test.js
COPY internal/webadmin/assets/catalog_assignment_helpers.js ./internal/webadmin/assets/catalog_assignment_helpers.js
COPY internal/webadmin/assets/catalog_assignment_helpers.test.js ./internal/webadmin/assets/catalog_assignment_helpers.test.js
COPY internal/webadmin/assets/clients.js ./internal/webadmin/assets/clients.js
COPY internal/webadmin/assets/sources.js ./internal/webadmin/assets/sources.js
COPY cmd/lanready-gui/frontend/dist/app.js ./cmd/lanready-gui/frontend/dist/app.js
COPY cmd/lanready-gui/frontend/dist/readiness.js ./cmd/lanready-gui/frontend/dist/readiness.js
COPY cmd/lanready-gui/frontend/dist/installs.js ./cmd/lanready-gui/frontend/dist/installs.js
COPY cmd/lanready-gui/frontend/dist/installs.test.js ./cmd/lanready-gui/frontend/dist/installs.test.js
COPY cmd/lanready-gui/frontend/dist/readiness.test.js ./cmd/lanready-gui/frontend/dist/readiness.test.js
COPY cmd/lanready-gui/frontend/dist/frontend_contract.test.js ./cmd/lanready-gui/frontend/dist/frontend_contract.test.js
COPY cmd/lanready-gui/frontend/dist/index.html ./cmd/lanready-gui/frontend/dist/index.html
RUN node --check internal/webadmin/assets/catalog.js
RUN node --check internal/webadmin/assets/catalog_cache_helpers.js
RUN node --check internal/webadmin/assets/catalog_assignment_helpers.js
RUN node --check internal/webadmin/assets/clients.js
RUN node --check internal/webadmin/assets/sources.js
RUN node --check cmd/lanready-gui/frontend/dist/app.js
RUN node --check cmd/lanready-gui/frontend/dist/readiness.js
RUN node --check cmd/lanready-gui/frontend/dist/installs.js
RUN node --test internal/webadmin/assets/catalog_cache_helpers.test.js
RUN node --test internal/webadmin/assets/catalog_assignment_helpers.test.js
RUN node --test cmd/lanready-gui/frontend/dist/readiness.test.js
RUN node --test cmd/lanready-gui/frontend/dist/installs.test.js
RUN node --test cmd/lanready-gui/frontend/dist/frontend_contract.test.js
RUN touch /web-test-ok

FROM golang:${GO_VERSION}-alpine3.24 AS build
WORKDIR /src
COPY --from=web-test /web-test-ok /tmp/web-test-ok
COPY go.mod ./
COPY go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY docs/contracts ./docs/contracts
ARG VERSION=dev
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/lanready-server ./cmd/lanready-server \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/lanready-manifest ./cmd/lanready-manifest \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/lanready-release ./cmd/lanready-release \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/lanready-signer ./cmd/lanready-signer \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/LANReady.exe ./cmd/lanready

# Export binaries without requiring Go on the host:
# docker build --target artifacts --output type=local,dest=./bin .
FROM scratch AS artifacts
COPY --from=build /out/ /

FROM alpine:3.24.1 AS manifest
RUN addgroup -g 10001 -S lanready && adduser -u 10001 -S -G lanready lanready
COPY --from=build /out/lanready-manifest /usr/local/bin/lanready-manifest
WORKDIR /work
USER 10001:10001
ENTRYPOINT ["lanready-manifest"]

FROM alpine:3.24.1 AS server
RUN addgroup -g 10001 -S lanready && adduser -u 10001 -S -G lanready lanready \
    && mkdir -p /data/events /data/content && chown -R lanready:lanready /data
COPY --from=build /out/lanready-server /usr/local/bin/lanready-server
COPY --from=build /src/docs/contracts/schemas/ /usr/share/lanready/schemas/
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["lanready-server"]
CMD ["-listen", ":8080", "-data", "/data"]

FROM alpine:3.24.1 AS signer
RUN addgroup -g 10001 -S lanready && adduser -u 10001 -S -G lanready lanready
COPY --from=build /out/lanready-release /usr/local/bin/lanready-release
COPY --from=build /src/docs/contracts/schemas/ /usr/share/lanready/schemas/
ENV LANREADY_RELEASE_SCHEMA_DIR=/usr/share/lanready/schemas
WORKDIR /work
USER 10001:10001
ENTRYPOINT ["lanready-release"]

FROM alpine:3.24.1 AS signer-service
RUN addgroup -g 10001 -S lanready && adduser -u 10001 -S -G lanready lanready
COPY --from=build /out/lanready-signer /usr/local/bin/lanready-signer
COPY --from=build /src/docs/contracts/schemas/ /usr/share/lanready/schemas/
ENV LANREADY_RELEASE_SCHEMA_DIR=/usr/share/lanready/schemas \
    LANREADY_SIGNER_SOCKET=/run/lanready-signer/signer.sock \
    LANREADY_RELEASE_PRIVATE_KEY_FILE=/run/secrets/event_release_private_key
USER 10001:10001
ENTRYPOINT ["lanready-signer"]
