# Litestream is built from its release source with our Go and every library at its latest
# version (third_party/litestream): its own release binary trails on both and fails the Trivy
# gate. Keep this version equal to LITESTREAM_VERSION in the Dockerfile.
LITESTREAM_VERSION := v0.5.17
GO_VERSION := $(shell sed -n 's/^go //p' go.mod)

# Tools run with `go run pkg@version`, so they stay out of go.mod and the binary.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
# Dependabot does not update this pin; bump it by hand.
TRIVY_IMAGE := aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c

.PHONY: check run image scan smoke trial trial-mac release litestream-src litestream-deps litestream-test

check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go run $(STATICCHECK) ./...
	go run $(GOVULNCHECK) ./...
	go test -race ./...

# A throwaway key for local work. Production keeps its key outside the repo and the data dir.
dev.key:
	go run ./cmd/saf keygen > dev.key

run: dev.key
	SAF_DATA_DIR=./data SAF_KEY_FILE=./dev.key go run ./cmd/saf serve

image:
	docker build -t saf:dev .

# The image for the GCP VM (x86-64), saved for shipping over SSH (docs/DEPLOY.md).
release:
	docker buildx build --platform linux/amd64 -t saf:release --load .
	mkdir -p dist
	docker save saf:release | gzip > dist/saf-release.tar.gz

# Clicks through the app in headless Chrome (needs Node 23+ and Chrome). Not in CI.
# SHOTS=<dir> also saves screenshots.
smoke:
	node test/browser/smoke.mjs

# Mandatory gate (docs/PLAN.md §3): fails on HIGH/CRITICAL findings that have a fix.
# The image goes in as a tar, so Trivy needs no access to the Docker socket.
scan: image
	docker save saf:dev -o .trivy-image.tar
	docker run --rm -v "$(CURDIR)/.trivy-image.tar:/image.tar:ro" -v saf-trivy-cache:/root/.cache $(TRIVY_IMAGE) \
		image --input /image.tar --no-progress --exit-code 1 --severity HIGH,CRITICAL --ignore-unfixed; \
		status=$$?; rm -f .trivy-image.tar; exit $$status

# The local trial for the pharmacy to test on Windows before the deploy: dist/SAF-teste.zip
# holds SAF-teste.exe (double-click: sample data, this computer only) and LEIA-ME.txt.
trial:
	rm -rf dist/SAF-teste dist/SAF-teste.zip
	mkdir -p dist/SAF-teste
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/SAF-teste/SAF-teste.exe ./cmd/saf
	cp docs/trial/LEIA-ME.txt dist/SAF-teste/
	cd dist && zip -qr SAF-teste.zip SAF-teste

# The same trial for an Apple Silicon Mac, to try it without Windows. Only the program is
# rebuilt: the trial's data in dist/SAF-teste-mac/saf-dados stays.
trial-mac:
	mkdir -p dist/SAF-teste-mac
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/SAF-teste-mac/SAF-teste ./cmd/saf

# Litestream's source at LITESTREAM_VERSION, from Go's module proxy, in .litestream-src.
litestream-src:
	rm -rf .litestream-src && mkdir .litestream-src
	cp -R "$$(go mod download -json github.com/benbjohnson/litestream@$(LITESTREAM_VERSION) | sed -n 's/^.*"Dir": "\(.*\)",$$/\1/p')"/. .litestream-src/
	chmod -R u+w .litestream-src

# Refreshes third_party/litestream: Litestream raised to our Go version, every library upgraded.
# Then run make litestream-test check scan.
litestream-deps: litestream-src
	cd .litestream-src && go mod edit -go=$(GO_VERSION) -toolchain=none && go get -u ./... && go mod tidy
	mkdir -p third_party/litestream
	cp .litestream-src/go.mod .litestream-src/go.sum third_party/litestream/
	rm -rf .litestream-src

# Builds Litestream with third_party/litestream and runs its own core and file-replica tests,
# so a library upgrade that breaks it shows up before the image does.
litestream-test: litestream-src
	cp third_party/litestream/go.mod third_party/litestream/go.sum .litestream-src/
	cd .litestream-src && CGO_ENABLED=0 go vet ./cmd/litestream && go test -short -count=1 . ./file/...
	rm -rf .litestream-src
