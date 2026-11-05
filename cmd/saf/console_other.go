//go:build !windows

package main

// useUTF8Console does nothing outside Windows: terminals there already speak UTF-8.
func useUTF8Console() {}
