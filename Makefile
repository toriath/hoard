VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))

.PHONY: build web test fmt run image notices

build: web ## build the single binary (web UI embedded)
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o hoard ./cmd/hoard

web: ## build the web UI into web/dist
	cd web && npm ci && npm run build

test: ## vet and test the server, the web UI and the release version logic
	go vet ./...
	go test -race ./...
	npm --prefix web test
	scripts/next-version_test.sh

fmt:
	gofmt -w .

run: build ## run locally with data in ./data
	HOARD_DATA_DIR=./data ./hoard

image: ## build the container image locally
	docker build --build-arg VERSION=$(VERSION) -t hoard:dev .

notices: ## write THIRD_PARTY_NOTICES.md (licences of everything that ships)
	scripts/third-party-notices.sh all > THIRD_PARTY_NOTICES.md
