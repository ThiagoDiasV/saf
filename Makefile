# Tools run with `go run pkg@version`, so they stay out of go.mod and the binary.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
# Dependabot does not update this pin; bump it by hand.
TRIVY_IMAGE := aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c

.PHONY: check run smoke scan

check:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
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
# executable and fails on HIGH/CRITICAL findings that have a fix. Needs Docker.
scan:
	mkdir -p dist/scan
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/scan/saf.exe ./cmd/saf
	docker run --rm -v "$(CURDIR)/dist/scan:/scan:ro" -v saf-trivy-cache:/root/.cache $(TRIVY_IMAGE) \
		rootfs --no-progress --exit-code 1 --severity HIGH,CRITICAL --ignore-unfixed /scan
