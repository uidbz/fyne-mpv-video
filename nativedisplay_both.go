//go:build (linux || freebsd || netbsd || openbsd) && !x11 && !wayland

package main

import (
	"unsafe"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// nativeDisplay returns the native display handle for mpv's render context so it
// can set up zero-copy GPU-decode interop. This is the default build, where GLFW
// links both X11 and Wayland, so the platform is chosen at runtime.
//
// The returned code is 1 for X11, 2 for Wayland, 0 for none; disp is the
// matching Display*/wl_display* as an opaque pointer (nil when unknown).
func nativeDisplay() (int, unsafe.Pointer) {
	switch glfw.GetPlatform() {
	case glfw.PlatformX11:
		return 1, unsafe.Pointer(glfw.GetX11Display())
	case glfw.PlatformWayland:
		return 2, unsafe.Pointer(glfw.GetWaylandDisplay())
	default:
		return 0, nil
	}
}
