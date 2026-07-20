VERSION ?= 0.1.0
RELEASE_PUBLIC_KEY ?= $(shell tr -d '\r\n' < keys/lanready-public.key)
AUTHENTICODE_PUBLISHER_SHA256 ?=
CERTIFICATE_THUMBPRINT ?=
TIMESTAMP_URL ?=
POWERSHELL_BIN ?= pwsh
BASE_LDFLAGS := -s -w -X main.version=$(VERSION)
GUI_LDFLAGS := $(BASE_LDFLAGS) -X main.releasePublicKey=$(RELEASE_PUBLIC_KEY) -X main.authenticodePublisherSHA256=$(AUTHENTICODE_PUBLISHER_SHA256)
WAILS_PRODUCT_VERSION := $(shell node -p "require('./cmd/lanready-gui/wails.json').info.productVersion")

.PHONY: all test web-test vet docs-check clean windows windows-gui windows-package check-windows-version linux

all: test windows linux

test: docs-check web-test
	go test ./...

web-test:
	node --check internal/webadmin/assets/catalog.js
	node --check internal/webadmin/assets/catalog_cache_helpers.js
	node --check internal/webadmin/assets/catalog_assignment_helpers.js
	node --check internal/webadmin/assets/clients.js
	node --check internal/webadmin/assets/sources.js
	node --check cmd/lanready-gui/frontend/dist/app.js
	node --check cmd/lanready-gui/frontend/dist/readiness.js
	node --test internal/webadmin/assets/catalog_cache_helpers.test.js
	node --test internal/webadmin/assets/catalog_assignment_helpers.test.js
	node --test cmd/lanready-gui/frontend/dist/readiness.test.js
	node --test cmd/lanready-gui/frontend/dist/frontend_contract.test.js

vet:
	go vet ./...

docs-check:
	python3 scripts/verify_docs.py
	python3 -m unittest scripts.verify_docs_test scripts.lanready_secrets_test scripts.lanready_compose_paths_test

windows:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(BASE_LDFLAGS)" -o bin/LANReady.exe ./cmd/lanready

check-windows-version:
	@test "$(VERSION)" = "$(WAILS_PRODUCT_VERSION)" || (echo "VERSION=$(VERSION) stimmt nicht mit wails.json productVersion=$(WAILS_PRODUCT_VERSION) überein"; exit 1)

windows-gui: check-windows-version
	cd cmd/lanready-gui && wails build -platform windows/amd64 -s -skipbindings -skipembedcreate -m -trimpath -webview2 download -ldflags "$(GUI_LDFLAGS)" -o LANReady.exe

windows-package: check-windows-version
	@test -n "$(AUTHENTICODE_PUBLISHER_SHA256)" || (echo "AUTHENTICODE_PUBLISHER_SHA256 ist für ein Releasepaket zwingend"; exit 1)
	@test -n "$(CERTIFICATE_THUMBPRINT)" || (echo "CERTIFICATE_THUMBPRINT ist für ein Releasepaket zwingend"; exit 1)
	@test -n "$(TIMESTAMP_URL)" || (echo "TIMESTAMP_URL ist für ein Releasepaket zwingend"; exit 1)
	$(POWERSHELL_BIN) -NoProfile -File scripts/build-windows-release.ps1 -Version "$(VERSION)" -ReleasePublicKey "$(RELEASE_PUBLIC_KEY)" -PublisherCertificateSHA256 "$(AUTHENTICODE_PUBLISHER_SHA256)" -CertificateThumbprint "$(CERTIFICATE_THUMBPRINT)" -TimestampUrl "$(TIMESTAMP_URL)"

linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(BASE_LDFLAGS)" -o bin/lanready-server ./cmd/lanready-server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(BASE_LDFLAGS)" -o bin/lanready-manifest ./cmd/lanready-manifest
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(BASE_LDFLAGS)" -o bin/lanready-release ./cmd/lanready-release

clean:
	rm -rf bin dist
