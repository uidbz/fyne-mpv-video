package main

/*
#cgo pkg-config: mpv
#include <mpv/client.h>
#include <mpv/render.h>
#include <mpv/render_gl.h>
#include <stdlib.h>

// Forward declarations of Go-exported callbacks.
void *goGetProcAddress(void *ctx, char *name);
void goRenderUpdate(void *ctx);

// Trampoline used as mpv's get_proc_address: mpv passes a plain C string; we
// hand it to Go, which resolves it via GLFW.
static void *get_proc_address_bridge(void *ctx, const char *name) {
    return goGetProcAddress(ctx, (char *)name);
}

// Build the OpenGL init params param array. Cgo cannot take the address of a C
// function from Go, so we assemble the struct here.
static mpv_render_param *make_gl_init_params(void *proc_ctx) {
    mpv_opengl_init_params *gl = calloc(1, sizeof(mpv_opengl_init_params));
    gl->get_proc_address = get_proc_address_bridge;
    gl->get_proc_address_ctx = proc_ctx;

    mpv_render_param *params = calloc(4, sizeof(mpv_render_param));
    static int api_type_len; // unused placeholder to keep layout obvious
    (void)api_type_len;

    params[0].type = MPV_RENDER_PARAM_API_TYPE;
    params[0].data = MPV_RENDER_API_TYPE_OPENGL;
    params[1].type = MPV_RENDER_PARAM_OPENGL_INIT_PARAMS;
    params[1].data = gl;
    params[2].type = MPV_RENDER_PARAM_ADVANCED_CONTROL;
    static int yes = 1;
    params[2].data = &yes;
    params[3].type = 0;
    params[3].data = NULL;
    return params;
}

static void free_gl_init_params(mpv_render_param *params) {
    if (!params) return;
    if (params[1].data) free(params[1].data);
    free(params);
}

// Render the current frame into the given FBO. flip_y=1 renders with a flipped
// Y axis so the result matches Fyne's top-left texture origin convention.
static int render_to_fbo(mpv_render_context *ctx, int fbo, int w, int h) {
    mpv_opengl_fbo target;
    target.fbo = fbo;
    target.w = w;
    target.h = h;
    target.internal_format = 0;

    int flip_y = 1;
    mpv_render_param params[3];
    params[0].type = MPV_RENDER_PARAM_OPENGL_FBO;
    params[0].data = &target;
    params[1].type = MPV_RENDER_PARAM_FLIP_Y;
    params[1].data = &flip_y;
    params[2].type = 0;
    params[2].data = NULL;
    return mpv_render_context_render(ctx, params);
}

static void set_update_callback(mpv_render_context *ctx, void *go_ctx) {
    mpv_render_context_set_update_callback(ctx, goRenderUpdate, go_ctx);
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/go-gl/glfw/v3.4/glfw"
)

// mpvPlayer drives a libmpv instance and renders its video into an OpenGL FBO
// via the Render API. It implements canvas.GLVideoRenderer.
type mpvPlayer struct {
	mpv    *C.mpv_handle
	render *C.mpv_render_context

	initOnce sync.Once
	initErr  error

	needsPaint atomic.Bool
	onUpdate   func() // called (from any thread) when a new frame is ready

	self cgo.Handle // stable handle passed to C callbacks
	file string
}

func newMPVPlayer(file string) (*mpvPlayer, error) {
	h := C.mpv_create()
	if h == nil {
		return nil, fmt.Errorf("mpv_create failed")
	}
	if err := checkMPV(C.mpv_initialize(h)); err != nil {
		C.mpv_terminate_destroy(h)
		return nil, err
	}
	// Let mpv choose hardware decoding when available.
	setOption(h, "hwdec", "auto-safe")
	setOption(h, "vo", "libmpv")

	p := &mpvPlayer{mpv: h, file: file}
	p.self = cgo.NewHandle(p)
	return p, nil
}

// SetOnUpdate registers a callback invoked whenever mpv signals a new frame.
func (p *mpvPlayer) SetOnUpdate(fn func()) { p.onUpdate = fn }

// ensureRender lazily creates the render context. It must run on the GL thread
// with the context current, which is guaranteed because it is only called from
// RenderInto (invoked by Fyne's painter during a paint pass).
func (p *mpvPlayer) ensureRender() error {
	p.initOnce.Do(func() {
		handle := unsafe.Pointer(uintptr(p.self))
		params := C.make_gl_init_params(handle)
		defer C.free_gl_init_params(params)

		var rctx *C.mpv_render_context
		if err := checkMPV(C.mpv_render_context_create(&rctx, p.mpv, params)); err != nil {
			p.initErr = err
			return
		}
		p.render = rctx
		C.set_update_callback(rctx, handle)

		// Start playback now that the render context exists.
		p.command("loadfile", p.file)
	})
	return p.initErr
}

// RenderInto implements canvas.GLVideoRenderer.
func (p *mpvPlayer) RenderInto(fbo uint32, width, height int) {
	if err := p.ensureRender(); err != nil {
		return
	}
	p.needsPaint.Store(false)
	C.render_to_fbo(p.render, C.int(fbo), C.int(width), C.int(height))
}

// NeedsPaint implements canvas.GLVideoRenderer.
func (p *mpvPlayer) NeedsPaint() bool { return p.needsPaint.Load() }

// Aspect implements canvas.GLVideoRenderer, returning the display aspect ratio.
func (p *mpvPlayer) Aspect() float32 {
	a, err := p.getPropertyDouble("video-params/aspect")
	if err != nil || a <= 0 {
		return 0
	}
	return float32(a)
}

// Play resumes playback.
func (p *mpvPlayer) Play() { p.setPropertyFlag("pause", false) }

// Pause halts playback, leaving the current frame shown.
func (p *mpvPlayer) Pause() { p.setPropertyFlag("pause", true) }

// TogglePause flips the paused state and returns the new paused value.
func (p *mpvPlayer) TogglePause() bool {
	paused := !p.IsPaused()
	p.setPropertyFlag("pause", paused)
	return paused
}

// IsPaused reports whether playback is currently paused.
func (p *mpvPlayer) IsPaused() bool {
	v, err := p.getPropertyFlag("pause")
	return err == nil && v
}

// Position returns the current playback time in seconds.
func (p *mpvPlayer) Position() float64 {
	v, _ := p.getPropertyDouble("time-pos")
	return v
}

// Duration returns the total media length in seconds, or 0 if unknown.
func (p *mpvPlayer) Duration() float64 {
	v, _ := p.getPropertyDouble("duration")
	return v
}

// SeekTo jumps to an absolute position in seconds.
func (p *mpvPlayer) SeekTo(seconds float64) {
	p.command("seek", strconv.FormatFloat(seconds, 'f', 3, 64), "absolute")
}

// Close stops playback and releases all mpv resources. Safe to call once.
func (p *mpvPlayer) Close() {
	if p.render != nil {
		C.mpv_render_context_free(p.render)
		p.render = nil
	}
	if p.mpv != nil {
		C.mpv_terminate_destroy(p.mpv)
		p.mpv = nil
	}
	if p.self != 0 {
		p.self.Delete()
		p.self = 0
	}
}

func (p *mpvPlayer) getPropertyDouble(name string) (float64, error) {
	if p.mpv == nil {
		return 0, fmt.Errorf("mpv closed")
	}
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var val C.double
	if err := checkMPV(C.mpv_get_property(p.mpv, cn, C.MPV_FORMAT_DOUBLE, unsafe.Pointer(&val))); err != nil {
		return 0, err
	}
	return float64(val), nil
}

func (p *mpvPlayer) getPropertyFlag(name string) (bool, error) {
	if p.mpv == nil {
		return false, fmt.Errorf("mpv closed")
	}
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var val C.int
	if err := checkMPV(C.mpv_get_property(p.mpv, cn, C.MPV_FORMAT_FLAG, unsafe.Pointer(&val))); err != nil {
		return false, err
	}
	return val != 0, nil
}

func (p *mpvPlayer) setPropertyFlag(name string, value bool) {
	if p.mpv == nil {
		return
	}
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var val C.int
	if value {
		val = 1
	}
	C.mpv_set_property(p.mpv, cn, C.MPV_FORMAT_FLAG, unsafe.Pointer(&val))
}

func (p *mpvPlayer) command(args ...string) {
	cargs := make([]*C.char, len(args)+1)
	for i, a := range args {
		cargs[i] = C.CString(a)
	}
	cargs[len(args)] = nil
	C.mpv_command(p.mpv, &cargs[0])
	for i := range args {
		C.free(unsafe.Pointer(cargs[i]))
	}
}

func setOption(h *C.mpv_handle, name, value string) {
	cn := C.CString(name)
	cv := C.CString(value)
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cv))
	C.mpv_set_option_string(h, cn, cv)
}

func checkMPV(status C.int) error {
	if status >= 0 {
		return nil
	}
	return fmt.Errorf("mpv error: %s", C.GoString(C.mpv_error_string(status)))
}

//export goGetProcAddress
func goGetProcAddress(ctx unsafe.Pointer, name *C.char) unsafe.Pointer {
	return unsafe.Pointer(glfw.GetProcAddress(C.GoString(name)))
}

//export goRenderUpdate
func goRenderUpdate(ctx unsafe.Pointer) {
	p := cgo.Handle(uintptr(ctx)).Value().(*mpvPlayer)
	p.needsPaint.Store(true)
	if p.onUpdate != nil {
		p.onUpdate()
	}
}
