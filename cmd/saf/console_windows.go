package main

import "syscall"

// useUTF8Console makes the Windows console show "versão" and "Só" instead of garbage: older
// consoles start in a legacy code page.
func useUTF8Console() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001)
}
