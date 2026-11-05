#!/bin/sh
# Builds the Windows desktop app at $1: SAF.exe with its icon, a DPI-aware manifest (sharp text on
# scaled screens) and no console window. Used by make windows, make scan and the Windows CI job.
set -eu
go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --manifest gui --icon build/icon.png \
  --product-name SAF --file-description "SAF - Servico de Apoio a Farmacia" --out cmd/saf/rsrc
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags desktop,production -trimpath \
  -ldflags="-s -w -H windowsgui" -o "$1" ./cmd/saf
