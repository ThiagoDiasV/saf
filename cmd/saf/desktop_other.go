//go:build !desktop

package main

import "errors"

// desktopBuild is false in the plain build (saf serve, the commands); see desktop.go.
const desktopBuild = false

func desktop() error {
	return errors.New("this build has no window; build with -tags desktop,production")
}
