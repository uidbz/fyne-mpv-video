//go:build !linux && !freebsd && !netbsd && !openbsd

package main

import "unsafe"

// nativeDisplay reports no native display. On macOS and Windows mpv's OpenGL
// render context does not take an X11/Wayland display param; hardware decode is
// negotiated through the platform's own GL/GPU interop without one.
func nativeDisplay() (int, unsafe.Pointer) {
	return 0, nil
}
