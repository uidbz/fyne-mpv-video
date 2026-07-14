//go:build (linux || freebsd || netbsd || openbsd) && x11

package main

import (
	"unsafe"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// nativeDisplay returns the X11 Display* for mpv's render context. This build
// (-tags x11) links only X11 in GLFW, so only the X11 accessor is available.
func nativeDisplay() (int, unsafe.Pointer) {
	return 1, unsafe.Pointer(glfw.GetX11Display())
}
