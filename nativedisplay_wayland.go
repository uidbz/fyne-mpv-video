//go:build (linux || freebsd || netbsd || openbsd) && wayland

package main

import (
	"unsafe"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// nativeDisplay returns the wl_display* for mpv's render context. This build
// (-tags wayland) links only Wayland in GLFW, so only the Wayland accessor is
// available. This is the path where zero-copy VAAPI/dmabuf interop matters most.
func nativeDisplay() (int, unsafe.Pointer) {
	return 2, unsafe.Pointer(glfw.GetWaylandDisplay())
}
