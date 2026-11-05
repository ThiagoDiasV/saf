#!/bin/sh
# Builds the Windows desktop app at $1: SAF.exe with its icon, a DPI-aware manifest (sharp text on
# scaled screens) and no console window. VERSION (e.g. 1.4.2) marks a release; without it the build
# is "dev" and never updates itself. Used by make windows, make scan, make release-exe and CI.
set -eu
version=${VERSION:-dev}
details=""
if [ "$version" != dev ]; then
  details="--product-version $version --file-version $version"
fi
go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --manifest gui --icon build/icon.png \
  --product-name SAF --file-description "SAF - Servico de Apoio a Farmacia" $details --out cmd/saf/rsrc
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags desktop,production -trimpath \
  -ldflags="-s -w -H windowsgui -X main.version=$version" -o "$1" ./cmd/saf
