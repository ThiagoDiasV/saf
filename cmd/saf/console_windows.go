package main

import (
	"log"
	"os"
	"syscall"
	"unsafe"
)

// attachConsole lets the commands (create-admin, keygen) print in the cmd window they were typed
// in: Windows starts the desktop build, a GUI program, without one. Output already going to a
// console, a pipe or a file is left alone.
func attachConsole() {
	if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != syscall.InvalidHandle {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	const attachParentProcess = uintptr(^uint32(0))
	if ok, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess); ok == 0 {
		return
	}
	kernel32.NewProc("SetConsoleOutputCP").Call(65001) // UTF-8, so "já" and "Senha" print right
	out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = out, out
	log.SetOutput(out) // slog's default writer was bound to the old stderr
}

// showError tells the person who double-clicked SAF.exe why it didn't open: a GUI program has
// no console to print to.
func showError(err error) {
	text, _ := syscall.UTF16PtrFromString("O SAF não abriu.\n\n" + err.Error())
	title, _ := syscall.UTF16PtrFromString("SAF")
	const iconError = 0x10
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), iconError)
}
