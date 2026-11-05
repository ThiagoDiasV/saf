# Tools run with `go run pkg@version`, so they stay out of go.mod and the binary.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
# Dependabot does not update this pin; bump it by hand.
TRIVY_IMAGE := aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c
# $(call trivy,<dir>) fails on HIGH/CRITICAL findings that have a fix in the programs in <dir>.
trivy = docker run --rm -v "$(CURDIR)/$(1):/scan:ro" -v saf-trivy-cache:/root/.cache $(TRIVY_IMAGE) \
	rootfs --no-progress --exit-code 1 --severity HIGH,CRITICAL --ignore-unfixed /scan

# A release version, e.g. make windows VERSION=1.4.2. "dev" builds never update themselves.
VERSION ?= dev

.PHONY: check run smoke scan windows mac release-exe

check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	GOOS=windows go vet -tags desktop,production ./cmd/saf
	go run $(STATICCHECK) ./...
	go run $(GOVULNCHECK) ./...
	go test -race ./...

# A throwaway key for local work. The pharmacy's key never enters the repo.
dev.key:
	go run ./cmd/saf keygen > dev.key

run: dev.key
	SAF_DATA_DIR=./data SAF_KEY_FILE=./dev.key go run ./cmd/saf serve

# Clicks through the app in headless Chrome (needs Node 23+ and Chrome). Not in CI.
# SHOTS=<dir> also saves screenshots.
smoke:
	node test/browser/smoke.mjs

# Mandatory gate (docs/PLAN.md §3): Trivy reads the library versions built into the Windows
# desktop app and fails on HIGH/CRITICAL findings that have a fix. Needs Docker.
scan:
	build/windows.sh dist/scan/SAF.exe
	$(call trivy,dist/scan)

# The desktop app for the pharmacy's PC: dist/SAF-windows.zip holds the folder SAF with SAF.exe.
# Unzipped to C:\, SAF keeps its key and data next to it in C:\SAF.
windows:
	VERSION=$(VERSION) build/windows.sh dist/SAF/SAF.exe
	cd dist && rm -f SAF-windows.zip && zip -qr SAF-windows.zip SAF/SAF.exe

# The same app for an Apple Silicon Mac, to check builds (needs Xcode's Command Line Tools).
# Its key and data go next to it, in dist/.
mac:
	mkdir -p dist/SAF.app/Contents/MacOS dist/SAF.app/Contents/Resources
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -trimpath \
		-ldflags="-s -w" -o dist/SAF.app/Contents/MacOS/SAF ./cmd/saf
	cp build/darwin/Info.plist dist/SAF.app/Contents/
	cp build/darwin/icon.icns dist/SAF.app/Contents/Resources/

# A release's SAF.exe, scanned; the release job then signs it (.github/workflows/release.yml).
release-exe:
	@test "$(VERSION)" != dev || { echo "usage: make release-exe VERSION=1.4.2"; exit 1; }
	VERSION=$(VERSION) build/windows.sh dist/release/SAF.exe
	$(call trivy,dist/release)
