# Embedding mpv Video in Fyne on Wayland — Full Documentation

This document explains, in complete detail, how video playback was added to a
[Fyne](https://fyne.io) application by embedding [libmpv](https://mpv.io) — and
crucially, how this was made to work on **Wayland**, where the traditional
"embed a video window into another window" trick does not exist.

It is written to be read top to bottom by someone who has never seen this code.
Every file, every function, and every non-obvious decision is described.

---

## Table of contents

1. [The problem, and why it is hard on Wayland](#1-the-problem)
2. [The solution in one paragraph](#2-the-solution)
3. [How Fyne renders (the fact that makes this possible)](#3-how-fyne-renders)
4. [Architecture overview](#4-architecture-overview)
5. [The data/control flow of a single frame](#5-frame-flow)
6. [File-by-file reference](#6-file-by-file)
7. [The tricky details](#7-tricky-details)
8. [Build & run instructions](#8-build-and-run)
9. [What was verified, and what is still open](#9-status)
10. [Glossary](#10-glossary)

---

## 1. The problem

**Goal:** show a playing video inside a Fyne window/container/widget, and have
it work on Wayland, ideally by embedding mpv.

**Why the "obvious" approach fails on Wayland.** The classic way to embed a
video player is *window embedding*: you create a child OS window and tell mpv to
render into it by passing its window ID (X11's `--wid` option). The host GUI
reserves a rectangle, and the video player draws into that rectangle as a
separate native window.

This model **does not exist on Wayland**. Wayland deliberately has no concept of
"here is a numeric ID of an arbitrary window belonging to another process; draw
into it." A Wayland client can only manage its own surfaces. The only sanctioned
way to composite another process's output is an explicit subsurface protocol
plus a nested compositor — heavy, fragile, and not something a toolkit like Fyne
exposes. This is the crux of the long-standing requests:

- Fyne feature request: <https://github.com/fyne-io/fyne/issues/449>
- libmpv on Wayland: <https://github.com/mpv-player/mpv/issues/1242>

So `--wid`-style embedding is a dead end here.

---

## 2. The solution

**Do not embed a window at all. Embed a *texture*.**

libmpv offers a second, completely different integration path called the
**Render API** (`mpv/render.h` + `mpv/render_gl.h`). Instead of creating its own
window, libmpv renders each video frame into an **OpenGL framebuffer object
(FBO)** that *you* own, using *your* OpenGL context. You then draw that FBO's
colour texture wherever you like inside your own UI.

Because this never touches the windowing system — it is pure OpenGL — it behaves
identically on X11, Wayland, macOS and Windows. The Wayland `--wid` problem
simply never arises: there is no second window to embed.

This is exactly how mpv is embedded into Qt, GTK, and other toolkits. We apply
the same technique to Fyne.

---

## 3. How Fyne renders

The whole approach hinges on one fact discovered by reading the Fyne source in
`./fyne`:

> **Fyne draws everything through OpenGL.**

The desktop driver uses [GLFW](https://www.glfw.org) for windowing and
[go-gl](https://github.com/go-gl/gl) for OpenGL. Relevant pieces:

- **The render loop** lives in `fyne/internal/driver/glfw/loop.go`. The function
  `repaintWindow(w)` (around line 214) is called when a window is dirty. It:
  1. ensures the minimum size,
  2. frees dirty textures,
  3. calls `updateGLContext(w)` (makes the window's GL context current and sets
     the viewport/output size),
  4. calls `canvas.paint(size)`, which walks the object tree and paints each
     object,
  5. calls `view.SwapBuffers()` to present.

- **The painter** lives in `fyne/internal/painter/gl/`. `canvas.paint` ends up
  in `painter.Paint(obj, …)` → `drawObject(obj, …)` in `draw.go`. `drawObject`
  is a big **type switch**: for each concrete canvas object type
  (`*canvas.Image`, `*canvas.Rectangle`, `*canvas.Text`, `*canvas.Shader`, …) it
  calls a matching `draw*` method.

- **Single-threaded GL.** All painting happens on one dedicated thread with the
  GL context current. This matters enormously, because libmpv's OpenGL Render
  API has a hard rule: **all `mpv_render_*` calls must happen on the thread that
  owns the GL context.** Fyne's design gives us that thread for free — anything
  we do inside a `draw*` method is already on it.

Two consequences shaped the design:

1. We can add a **new canvas object type** and a **new `draw*` case**, and our
   code will be invoked on the correct thread with the context current.
2. Fyne only repaints when something is marked *dirty*. Video produces frames
   continuously, so we must **mark the object dirty every time mpv has a new
   frame**, or playback would freeze after the first frame.

---

## 4. Architecture overview

The implementation is split deliberately across **two modules**:

```
fynempv/
├── fyne/                       ← the Fyne fork (patched)
│   ├── canvas/glvideo.go               NEW  – the GLVideo object + interface
│   └── internal/painter/gl/
│       ├── painter.go                  EDIT – FBO cache fields + cleanup hook
│       ├── draw.go                     EDIT – one new type-switch case
│       ├── glvideo_desktop.go          NEW  – the real FBO drawing (desktop GL)
│       └── glvideo_other.go            NEW  – no-op stub (mobile/web/ES)
│
└── demo/                       ← the application (all libmpv/cgo lives here)
    ├── go.mod                          replace fyne.io/fyne/v2 => ../fyne
    ├── mpv.go                          NEW  – libmpv cgo bindings + Render API
    ├── video_widget.go                 NEW  – reusable Video widget + controls
    └── main.go                         NEW  – tiny entry point
```

**The key architectural rule: the Fyne fork contains *no* mpv or cgo code.**

The fork only knows about an abstract interface, `canvas.GLVideoRenderer`, which
says "something can draw a frame into an FBO." libmpv is one implementation of
that interface, and it lives entirely in the application (`demo/`). This means:

- The fork change is small, generic, and reusable — any GL video source (mpv,
  GStreamer, a custom decoder, a WebRTC frame pipeline) can implement the same
  interface.
- The heavyweight, platform-specific cgo dependency (libmpv) is confined to the
  app, so building the toolkit itself never requires libmpv.

The boundary looks like this:

```
   ┌──────────────────────── fyne fork ────────────────────────┐
   │  canvas.GLVideo (a CanvasObject)                           │
   │     └─ holds a canvas.GLVideoRenderer  ◄── interface only  │
   │  painter.drawGLVideo:                                      │
   │     • owns an offscreen FBO + texture per object           │
   │     • calls renderer.RenderInto(fbo, w, h)                 │
   │     • composites the texture into the widget's bounds      │
   └───────────────────────────┬───────────────────────────────┘
                                │ implements GLVideoRenderer
   ┌───────────────────────────┴──────────── demo app ─────────┐
   │  mpvPlayer (cgo → libmpv)                                  │
   │     • RenderInto → mpv_render_context_render(fbo)          │
   │     • NeedsPaint / Aspect                                  │
   │     • Play/Pause/Seek/Position/Duration/Close              │
   │  Video widget: GLVideo + play button + seek bar + clock    │
   └────────────────────────────────────────────────────────────┘
```

---

## 5. Frame flow

Here is the complete life of one video frame, end to end:

1. **mpv decodes a frame** on its own internal threads. When a new frame is
   ready to be drawn, libmpv invokes the *update callback* we registered
   (`goRenderUpdate`). This can happen on **any** thread.

2. **We mark "needs paint" and ask Fyne to repaint.** `goRenderUpdate` sets an
   atomic flag and calls the app-supplied `onUpdate` closure. In the demo, that
   closure is `func(){ fyne.Do(video.Refresh) }`. `fyne.Do` marshals the call
   onto Fyne's main/render thread; `video.Refresh()` marks the object dirty.

3. **Fyne repaints.** On its render thread, Fyne runs `repaintWindow`, makes the
   GL context current, and walks the tree. It reaches our `GLVideo` object and
   calls `painter.drawGLVideo`.

4. **We render mpv into our FBO.** `drawGLVideo`:
   - computes the on-screen rectangle (applying aspect-ratio letterboxing),
   - ensures an offscreen FBO + colour texture of the right pixel size exists,
   - saves the currently-bound framebuffer,
   - binds our FBO and calls `renderer.RenderInto(fbo, w, h)`, which calls
     `mpv_render_context_render` — mpv draws the frame into our texture,
   - restores Fyne's framebuffer and viewport.

5. **We composite the texture.** `drawGLVideo` calls `drawTextureRegion`, the
   same helper Fyne uses for images, to draw our texture into the widget's
   rectangle. Letterbox bars are simply the window background showing through
   where we did not draw.

6. **Fyne presents** with `SwapBuffers`. The frame is on screen.

7. **Repeat.** Because step 2 fires for every new mpv frame, the object stays
   dirty at the video's frame rate and playback is smooth.

---

## 6. File-by-file

### 6.1 `fyne/canvas/glvideo.go` (NEW)

This file adds the public, backend-neutral API to the toolkit.

**`GLVideoRenderer` interface** — the contract a frame source must satisfy:

```go
type GLVideoRenderer interface {
    RenderInto(fbo uint32, width, height int)
    NeedsPaint() bool
    Aspect() float32
}
```

- `RenderInto(fbo, width, height)` — the painter has bound an FBO with a colour
  texture attached and a `width × height` pixel viewport; the implementation
  draws the current frame into it. The documented origin is bottom-left (OpenGL
  and libmpv convention). The implementation may freely issue GL calls because
  the painter's context is current on this thread.
- `NeedsPaint()` — reports whether a new frame exists since the last render.
  (Provided for callers that want to poll rather than push; the demo drives
  repaints via the update callback instead, but the method rounds out the
  contract.)
- `Aspect()` — display aspect ratio (width/height), or `0` if unknown. The
  painter uses this for letterboxing. Returning `0` means "just fill the box."

**`GLVideo` struct** — the canvas object itself:

```go
type GLVideo struct {
    baseObject
    Renderer GLVideoRenderer
}
```

It embeds `baseObject` (the shared Fyne mixin providing size/position/hidden
state), exactly like `canvas.Shader` and other primitives do. It holds a single
`Renderer` field. `NewGLVideo(renderer)` constructs one.

The `Hide`, `Move`, `Refresh`, and `Resize` methods mirror the pattern used by
`canvas.Shader`: they update the base object and then request a repaint
(`repaint`) or refresh (`Refresh`) so the canvas redraws. `Move`/`Resize`
short-circuit when nothing changed to avoid needless repaints.

The `var _ fyne.CanvasObject = (*GLVideo)(nil)` line is a compile-time assertion
that `GLVideo` satisfies the `CanvasObject` interface.

> **Why this file has no mpv/cgo:** keeping the object and interface in `canvas`
> means the toolkit compiles with zero external video dependencies, and any
> backend can plug in.

---

### 6.2 `fyne/internal/painter/gl/painter.go` (EDITED)

Three small additions to the painter struct and lifecycle:

1. **New field `fbWidth int`** (next to the existing `fbHeight`). The painter
   already tracked the framebuffer *height* (used to flip Y for scissoring); we
   now also track the *width* so that after mpv clobbers the GL viewport we can
   restore Fyne's full-window viewport with `Viewport(0, 0, fbWidth, fbHeight)`.
   It is set in `SetOutputSize`, right beside `fbHeight`.

2. **New field `videoTargets map[*canvas.GLVideo]*videoTarget`** plus the
   `videoTarget` type:

   ```go
   type videoTarget struct {
       fbo    uint32
       tex    uint32
       width  int
       height int
   }
   ```

   This is the per-object cache of the offscreen framebuffer, its colour
   texture, and the pixel size they were allocated for. One entry per on-screen
   `GLVideo`. Keyed by the object pointer.

3. **Cleanup hook in `Free`.** Fyne calls `painter.Free(obj)` when an object's
   resources should be released. We added:

   ```go
   if video, ok := obj.(*canvas.GLVideo); ok {
       p.freeVideoTarget(video)
   }
   ```

   so the FBO and texture are deleted when the object goes away.

---

### 6.3 `fyne/internal/painter/gl/draw.go` (EDITED)

Exactly one new case in the `drawObject` type switch:

```go
case *canvas.GLVideo:
    p.drawGLVideo(obj, pos, frame)
```

This is the single line that wires our object into Fyne's existing render
dispatch. Everything else in `draw.go` is untouched.

---

### 6.4 `fyne/internal/painter/gl/glvideo_desktop.go` (NEW)

The real work, compiled **only for desktop OpenGL** (the build tag matches the
same platforms as `gl_core.go`: not gles/arm/mobile/wasm, plus non-mobile
darwin). It talks to `github.com/go-gl/gl/v2.1/gl` directly.

Why raw go-gl instead of the painter's abstract `context` interface? That
interface (in `context.go`) has no framebuffer-object methods
(`GenFramebuffers`, `BindFramebuffer`, `FramebufferTexture2D`, …). Rather than
widen that interface across all five backends, the FBO handling is isolated in
this one desktop-tagged file, with a no-op stub for the others (§6.5).

**`drawGLVideo(v, pos, frame)`** does, in order:

1. **Bail if no renderer.** `if v.Renderer == nil { return }`.

2. **Compute the letterboxed rectangle.** Starting from the object's full
   position/size, if `Aspect()` is non-zero it shrinks the drawn rectangle to
   preserve the video's aspect ratio and centres it:
   - object wider than video → **pillarbox** (bars left/right): reduce width,
     shift X.
   - object taller than video → **letterbox** (bars top/bottom): reduce height,
     shift Y.

3. **Convert to pixels.** Multiplies by `p.pixScale` (the combined canvas scale
   × framebuffer scale, for HiDPI) and rounds to whole pixels with
   `roundToPixel`. Bails if the result is ≤ 0.

4. **Ensure the FBO/texture** via `ensureVideoTarget` (below), sized to those
   pixels.

5. **Save the current framebuffer binding** into `prevFBO` with
   `glGetIntegerv(GL_FRAMEBUFFER_BINDING)`. This is important: Fyne renders to
   the window's *default* framebuffer, which is **not guaranteed to be 0** (on
   macOS it is typically non-zero). We must restore exactly what was bound, not
   assume 0.

6. **Bind our FBO and render mpv into it:**
   ```go
   gl.BindFramebuffer(gl.FRAMEBUFFER, target.fbo)
   v.Renderer.RenderInto(target.fbo, target.width, target.height)
   ```

7. **Restore Fyne's framebuffer and viewport:**
   ```go
   gl.BindFramebuffer(gl.FRAMEBUFFER, uint32(prevFBO))
   p.ctx.Viewport(0, 0, p.fbWidth, p.fbHeight)
   ```
   The viewport restore is necessary because mpv sets the viewport to the video
   size while rendering, and the mpv render docs explicitly list `glViewport` as
   state it does *not* restore.

8. **Composite** the video texture into the on-screen rectangle:
   ```go
   p.drawTextureRegion(Texture(target.tex), drawPos, drawSize, frame)
   ```
   `drawTextureRegion` is Fyne's own existing helper (used for image regions);
   reusing it means the video honours the same vertex/coordinate math as every
   other textured object.

**`ensureVideoTarget(v, width, height)`** — lazy allocation with resize:

- Lazily creates the `videoTargets` map.
- If an entry exists at the exact requested size, returns it unchanged (the hot
  path — no allocation per frame).
- Otherwise, on first use it generates one texture and one framebuffer
  (`glGenTextures` / `glGenFramebuffers`). On a size change it reuses the same GL
  object names and just reallocates storage.
- Sets the texture parameters: `LINEAR` min/mag filtering (smooth scaling) and
  `CLAMP_TO_EDGE` wrapping (no edge bleeding), then allocates RGBA storage of the
  requested size with a `nil` data pointer (uninitialised — mpv will fill it).
- Attaches the texture to the framebuffer's `COLOR_ATTACHMENT0`, again saving and
  restoring the previous framebuffer binding around the operation.

**`freeVideoTarget(v)`** — deletes the framebuffer and texture and removes the
map entry. Called from `painter.Free`.

---

### 6.5 `fyne/internal/painter/gl/glvideo_other.go` (NEW)

The complement build tag (everything the desktop file excludes: gles, arm,
mobile, wasm). It provides no-op `drawGLVideo` and `freeVideoTarget` methods so
the package still compiles on those targets — a `GLVideo` simply draws nothing
there. This keeps `drawObject`'s reference to `drawGLVideo` valid on every
platform without pulling FBO code into backends that may not support it the same
way.

---

### 6.6 `demo/mpv.go` (NEW) — the libmpv backend

This is the only file containing cgo and libmpv. It implements
`canvas.GLVideoRenderer` plus playback controls. It is in `package main` in the
demo module.

#### 6.6.1 The C preamble

The comment block above `import "C"` is compiled as C. `#cgo pkg-config: mpv`
tells cgo to pull compiler/linker flags from `pkg-config mpv` (so it finds
`-lmpv` and the headers). It includes `client.h`, `render.h`, `render_gl.h`.

It defines several small C helpers, needed because cgo has awkward edges around
function pointers and struct arrays:

- **`goGetProcAddress` / `goRenderUpdate`** — forward declarations of the two
  Go functions we export back to C (see §6.6.5). Note `goGetProcAddress` takes
  `char *` (not `const char *`) to match the signature cgo generates for the
  exported Go function; the trampoline casts away `const`.

- **`get_proc_address_bridge`** — a static C function matching the exact
  signature libmpv wants for `get_proc_address`. It just forwards to the Go
  `goGetProcAddress`. We need a real C function here because you cannot hand a Go
  function pointer directly to a C struct field.

- **`make_gl_init_params(proc_ctx)`** — allocates and fills the parameter array
  for `mpv_render_context_create`. It sets:
  - `MPV_RENDER_PARAM_API_TYPE` = `MPV_RENDER_API_TYPE_OPENGL`,
  - `MPV_RENDER_PARAM_OPENGL_INIT_PARAMS` = a heap `mpv_opengl_init_params`
    holding our `get_proc_address_bridge` and the opaque `proc_ctx`,
  - `MPV_RENDER_PARAM_ADVANCED_CONTROL` = 1 (lets mpv schedule renders precisely
    and only redraw when needed),
  - a terminating zero entry.
  Building this in C avoids Go having to take the address of the C function
  pointer or lay out the tagged-union `mpv_render_param` array itself.

- **`free_gl_init_params`** — frees the heap `mpv_opengl_init_params` and the
  array.

- **`render_to_fbo(ctx, fbo, w, h)`** — fills an `mpv_opengl_fbo` struct with our
  FBO id and size, sets `MPV_RENDER_PARAM_FLIP_Y = 1`, and calls
  `mpv_render_context_render`. **`FLIP_Y` is essential:** OpenGL's framebuffer
  origin is bottom-left, but Fyne samples textures with a top-left origin. Asking
  mpv to flip Y means the frame lands right-side-up when Fyne draws it.

- **`set_update_callback`** — wraps `mpv_render_context_set_update_callback`,
  registering `goRenderUpdate` with an opaque context pointer.

#### 6.6.2 The `mpvPlayer` struct

```go
type mpvPlayer struct {
    mpv    *C.mpv_handle
    render *C.mpv_render_context
    initOnce sync.Once
    initErr  error
    needsPaint atomic.Bool
    onUpdate   func()
    self cgo.Handle
    file string
}
```

- `mpv` — the core libmpv handle.
- `render` — the OpenGL render context (created lazily; see below).
- `initOnce`/`initErr` — guarantee the render context is created exactly once.
- `needsPaint` — atomic flag set by the update callback, cleared on render.
- `onUpdate` — app closure invoked when a new frame is ready.
- `self` — a `runtime/cgo.Handle` (see §7.2) so C can safely refer back to this
  Go object.
- `file` — the media path/URL to play.

#### 6.6.3 Construction: `newMPVPlayer(file)`

1. `C.mpv_create()` allocates the handle.
2. `C.mpv_initialize(h)` starts the core; on error we `mpv_terminate_destroy` and
   return.
3. Two options are set as strings:
   - `hwdec = auto-safe` — enable hardware decoding when it is safe to do so.
   - `vo = libmpv` — select the "video output" driver that renders via the Render
     API (rather than opening its own window). **This is what makes embedding
     work.**
4. A `cgo.Handle` for the player is created and stored in `self`.

Note the render context is *not* created here — see next.

#### 6.6.4 Lazy render-context creation: `ensureRender()`

Guarded by `initOnce`. On first call it builds the init params, calls
`mpv_render_context_create(&rctx, p.mpv, params)`, stores the context, registers
the update callback, and issues `loadfile <file>` to begin playback.

**Why lazy?** `mpv_render_context_create` and all subsequent render calls must
run on the thread that owns the GL context, with it current. The only place we
are guaranteed to be on that thread is inside `RenderInto` (called by Fyne's
painter). So we defer creation to the first `RenderInto`. This neatly sidesteps
all "which thread am I on?" problems without any extra thread plumbing.

#### 6.6.5 Interface methods

- **`RenderInto(fbo, w, h)`** — calls `ensureRender()`, clears `needsPaint`, then
  `C.render_to_fbo(...)`. This is the method Fyne's `drawGLVideo` calls.
- **`NeedsPaint()`** — returns the atomic flag.
- **`Aspect()`** — reads mpv's `video-params/aspect` property (a double). Returns
  0 when unknown (before the first frame is decoded), which the painter treats as
  "fill."

#### 6.6.6 Playback controls

Thin wrappers over mpv properties/commands:

- `Play` / `Pause` — set the `pause` flag property to false/true.
- `TogglePause` — flips `pause` and returns the new value.
- `IsPaused` — reads the `pause` flag.
- `Position` — reads `time-pos` (seconds, double).
- `Duration` — reads `duration` (seconds, double; 0 if unknown).
- `SeekTo(seconds)` — issues the `seek <seconds> absolute` command.

#### 6.6.7 Property/command helpers

- `getPropertyDouble(name)` / `getPropertyFlag(name)` — call `mpv_get_property`
  with `MPV_FORMAT_DOUBLE` / `MPV_FORMAT_FLAG` into a C variable, guarding against
  a closed handle.
- `setPropertyFlag(name, bool)` — `mpv_set_property` with `MPV_FORMAT_FLAG`.
- `command(args...)` — builds a NULL-terminated C string array from the Go
  strings, calls `mpv_command`, and frees each C string. Used by `loadfile` and
  `seek`.
- `setOption(h, name, value)` — `mpv_set_option_string`.
- `checkMPV(status)` — converts a negative mpv status code into a Go error via
  `mpv_error_string`.

#### 6.6.8 Teardown: `Close()`

Frees the render context (`mpv_render_context_free`), terminates the core
(`mpv_terminate_destroy`), and deletes the `cgo.Handle`. Each step is guarded so
`Close` is idempotent-ish (safe to call once; nils out the fields).

#### 6.6.9 The exported callbacks

- **`goGetProcAddress(ctx, name)`** (`//export`) — resolves an OpenGL function
  name to a pointer using `glfw.GetProcAddress`. libmpv calls this (via the C
  trampoline) to obtain every GL entry point it needs. Using GLFW's resolver
  guarantees mpv and Fyne share the *same* GL implementation and context.
- **`goRenderUpdate(ctx)`** (`//export`) — libmpv's "new frame" signal. Recovers
  the `*mpvPlayer` from the `cgo.Handle`, sets `needsPaint`, and calls `onUpdate`.
  May run on any thread, so it does no GL work — it only requests a repaint.

---

### 6.7 `demo/video_widget.go` (NEW) — the reusable `Video` widget

Wraps the raw `canvas.GLVideo` with playback controls into a proper Fyne widget.

**`videoController` interface** — what the widget needs from its backend:

```go
type videoController interface {
    canvas.GLVideoRenderer          // RenderInto / NeedsPaint / Aspect
    SetOnUpdate(func())
    Play(); Pause(); TogglePause() bool; IsPaused() bool
    Position() float64; Duration() float64; SeekTo(float64)
    Close()
}
```

`*mpvPlayer` satisfies this, but the widget only depends on the interface — so it
has no libmpv/cgo import and could drive any compatible backend.

**`Video` struct** embeds `widget.BaseWidget` and holds the `canvas.GLVideo`, a
play/pause `*widget.Button`, a `*widget.Slider` (the seek bar), a `*widget.Label`
(the clock), a `stop chan struct{}` for the ticker goroutine, and a `seeking`
bool.

**`NewVideo(player)`** builds the widget:

- Creates the `GLVideo` with a 320×180 minimum size.
- Creates the play button (starts with the pause icon, since playback begins
  immediately) wired to `togglePlay`.
- Creates the time label `"0:00 / 0:00"`.
- Creates the seek slider on range `[0,1]` with fine `Step = 0.001`. Its
  `OnChanged` sets the `seeking` flag (so the ticker stops fighting the user's
  drag); its `OnChangeEnded` performs the actual seek to `val × duration` and
  clears the flag.
- Registers the update callback: `player.SetOnUpdate(func(){ fyne.Do(v.video.Refresh) })`.
  This is the crucial "repaint on every new frame" wiring described in §5.
- Launches `go v.tick()`.

**`togglePlay()`** flips pause via the backend and swaps the button icon between
`MediaPlayIcon` and `MediaPauseIcon`.

**`tick()`** runs on its own goroutine, every 500 ms: it reads position and
duration and, via `fyne.Do` (to touch widgets on the right thread), updates the
clock label and — unless the user is mid-drag — moves the slider to
`pos/duration`. It exits when `stop` is closed.

**`Close()`** closes the `stop` channel (guarded so it is safe once) and calls
`player.Close()`, releasing all mpv resources.

**`CreateRenderer()`** lays out the widget: a `Border` container with the video
in the centre and, along the bottom, another `Border` holding the play button on
the left, the clock on the right, and the seek slider filling the middle.
`widget.NewSimpleRenderer` wraps that content.

**`formatTime(seconds)`** formats a float number of seconds as `m:ss`.

---

### 6.8 `demo/main.go` (NEW) — entry point

Minimal:

1. Requires one CLI argument: the video file or URL.
2. `newMPVPlayer(file)` — constructs the backend (fatal on error).
3. `app.NewWithID("io.fyne.mpvdemo")` — a Fyne app with an ID (the ID avoids the
   Preferences-API warning).
4. Creates the window and the `Video` widget.
5. `SetCloseIntercept` ensures `video.Close()` runs before the window closes, so
   mpv shuts down cleanly.
6. Sets the widget as content, sizes the window 800×520, and `ShowAndRun()`.

---

### 6.9 `demo/go.mod`

Standard module file with one important line:

```
replace fyne.io/fyne/v2 => ../fyne
```

This makes the demo build against the **local patched fork** rather than the
published Fyne, so the new `canvas.GLVideo` type is available. `go-gl/glfw` is a
direct dependency because `goGetProcAddress` calls `glfw.GetProcAddress`.

---

## 7. The tricky details

These are the non-obvious things that make or break the integration.

### 7.1 Everything mpv-render happens on Fyne's render thread

libmpv's OpenGL Render API mandates that `mpv_render_context_create`,
`mpv_render_context_render`, and `mpv_render_context_free` all run on the thread
that owns the GL context, with it current. We never spawn a render thread of our
own; instead we piggy-back on Fyne's painter thread by only ever calling these
functions from inside `RenderInto` (and lazily creating the context there too).
The only cross-thread action is the update callback, which does no GL work.

### 7.2 Passing a Go pointer to C safely (`cgo.Handle`)

C needs to call back into a specific `*mpvPlayer` (for `get_proc_address_ctx` and
the update callback's context). You **must not** hand a raw Go pointer to C and
have C store it — the Go garbage collector may move or collect it, and cgo's
pointer-passing rules forbid it. `runtime/cgo.Handle` solves this: it returns an
integer handle that is stable and keeps the object alive. We store it in `self`,
pass it to C as an opaque `void*` (`unsafe.Pointer(uintptr(p.self))`), and in the
callbacks recover the object with `cgo.Handle(uintptr(ctx)).Value()`. `Close`
deletes the handle. (`go vet` prints a benign "possible misuse of unsafe.Pointer"
note about the uintptr round-trip; this is the sanctioned `cgo.Handle` idiom and
is safe because the value is never dereferenced as a Go pointer.)

### 7.3 Y-flip

OpenGL framebuffers are bottom-left origin; Fyne samples textures top-left. We
pass `MPV_RENDER_PARAM_FLIP_Y = 1` so mpv writes the frame flipped, and it then
displays upright when Fyne composites the texture. Without it, the video would be
upside-down.

### 7.4 Saving and restoring the framebuffer binding

`drawGLVideo` reads `GL_FRAMEBUFFER_BINDING` before binding our FBO and restores
exactly that afterwards, rather than binding 0. Fyne's window framebuffer is not
guaranteed to be 0 (notably on macOS). We also restore the viewport, because mpv
changes it and the render docs say it will not put it back.

### 7.5 Repaint-on-new-frame

Fyne is a retained-mode toolkit: it only redraws dirty objects. Video is a
continuous stream, so the update callback → `fyne.Do(video.Refresh)` chain is
what keeps frames flowing. Remove it and you get a single frozen frame.

### 7.6 `fyne.Do` for thread affinity

mpv's callback and our ticker goroutine are not on Fyne's main thread. Touching
widgets or the canvas from the wrong thread is unsafe, so both go through
`fyne.Do`, which schedules the closure on the correct thread.

### 7.7 FBO reuse and resize

The per-object FBO/texture is allocated once and only reallocated when the
widget's pixel size changes (which includes HiDPI scale changes, since we size in
device pixels via `pixScale`). Steady-state playback allocates nothing per frame.

### 7.8 Aspect-ratio letterboxing

The video is never stretched. `drawGLVideo` fits the video's aspect inside the
widget box and centres it; the uncovered margins are just the background showing
through. When `Aspect()` returns 0 (before the first decoded frame) the video
temporarily fills the box, then snaps to the correct ratio once known.

### 7.9 The build-tag split

FBO code is desktop-only (`glvideo_desktop.go`) because the abstract painter
`context` interface has no framebuffer methods and ES/mobile/web GL differs. The
stub (`glvideo_other.go`) keeps every platform compiling. Extending to mobile/ES
would mean adding an ES implementation with the matching build tag.

---

## 8. Build and run

**Prerequisites:**

- Go (built/tested with 1.26; module declares 1.22).
- A C toolchain (gcc) — cgo is required.
- `libmpv-dev` (provides `libmpv.so` and headers; `pkg-config mpv` must work):
  ```
  sudo apt install libmpv-dev
  ```
- The usual Fyne/GLFW system deps (X11/Wayland/OpenGL dev packages).

**Build and run:**

```
cd demo
go build .
./fynempvdemo /path/to/video.mp4
```

You can also pass a URL that mpv understands. To generate a quick test clip:

```
ffmpeg -f lavfi -i testsrc=duration=30:size=640x360:rate=30 -pix_fmt yuv420p test.mp4
```

### 8.1 Build tags for Wayland-only / X-free machines

The default build links **both** X11 and Wayland, resolves OpenGL through GLX
(which needs `gl.pc`), and links **`-lGL`** (desktop GL). On a machine set up for
Wayland/EGL/GLES only — a very common Gentoo/embedded configuration — none of
those defaults are satisfiable, and you get one of two errors:

- *"`gl.pc` not found"* (a pkg-config failure), or
- *"`ld: cannot find -lGL`"* (a linker failure).

**These come from three independent places, each controlled by a different build
tag. Fixing only one leaves the others failing — which is why a partial set of
tags still errors.** The full map:

| Tag | Consumed by | Effect |
|------|-------------|--------|
| `wayland` | **GLFW** (`build.go`) | link only Wayland libs, not X11 |
| `egl` | **go-gl/gl** (`procaddr.go`) | resolve GL via `eglGetProcAddress`; `pkg-config: egl` instead of `gl` (avoids `gl.pc`) |
| `gles2` | **GLFW** (`build.go:43`) | link `-lGLESv2` instead of `-lGL` |
| `gles` | **Fyne painter + driver** | select the GLES backend (`gl_es.go` / `glfw_es.go`, using `go-gl/gl/v3.1/gles2`) |

Two subtle traps:

1. **`egl` alone does *not* remove `-lGL`.** The `egl` tag only affects go-gl's
   proc loader and its pkg-config; GLFW independently hard-links `-lGL` on Linux
   (`#cgo linux,!gles1,!gles2,!gles3,!vulkan LDFLAGS: -lGL`). You still need
   `gles2` to flip GLFW to `-lGLESv2`.
2. **`gles` and `gles2` are different tags read by different packages** — `gles`
   switches *Fyne's* painter, `gles2` switches *GLFW's* linker. On a GLES-only box
   you need **both**, plus `egl` (so the go-gl `v3.1/gles2` package also uses
   `egl.pc` rather than `gl.pc`).

**Recommended command for a Wayland + EGL/GLES box (e.g. Gentoo with libglvnd
built without the X USE flag):**

```
go build -tags "wayland egl gles gles2" .
```

Verified: this produces a binary whose only GL/display `NEEDED` libraries are
`libEGL.so.1` and `libwayland-client.so.0` — **no `libGL.so`, no `libX11`** from
the toolkit. (libmpv may still pull X transitively unless mpv itself is built
X-free; see §7 discussion.)

If you are on a desktop-GL/X11 machine and only hit the `gl.pc` error, the
lighter fix is just `-tags "wayland egl"` (keeps the desktop-GL painter), or
install the `gl.pc` provider (`libgl-dev` on Debian/Ubuntu).

#### Why `gl.pc` is missing on Gentoo

`media-libs/libglvnd` built **without the X USE flag** deliberately installs
neither `gl.pc` **nor** the `libGL.so` linker symlink — both belong to the old
Mesa/GLX desktop-GL path. It ships `egl.pc`, `glesv2.pc`, and `opengl.pc`
instead. This is expected, not a broken config: the machine simply has EGL +
GLESv2 and no GLX dev files, so the correct answer is to point the whole stack at
GLES/EGL with the four tags above rather than to force desktop GL back in.

#### Video under the GLES painter

The `gles` tag switches Fyne to its GLES painter, which excludes the
desktop-tagged `glvideo_desktop.go`. To keep video working there, the fork also
ships **`glvideo_gles.go`** — the same FBO/letterbox logic built against the
`go-gl/gl/v3.1/gles2` bindings (framebuffer objects are core in GLES 2.0, so the
calls are identical). The build tags are arranged so exactly one implementation
compiles per target:

- desktop core → `glvideo_desktop.go`
- GLES (`gles`/arm) → `glvideo_gles.go`
- everything else (mobile, wasm) → `glvideo_other.go` (no-op stub)

Verified: `-tags "gles gles2 egl"` compiles the painter and the demo, and video
renders correctly through the GLES path (tested on X11 by omitting the `wayland`
tag; add `wayland` back for the Wayland target).

`-tags egl`/EGL is also the more appropriate base for future Wayland **hardware
decoding** in mpv, since that path wants EGL (see §9 item 2).

---

## 9. Status

**Verified working (on this machine, an X11 session):**

- Video renders live inside the Fyne window via the libmpv OpenGL Render API.
- Playback advances (confirmed by the moving test pattern and frame counter
  across successive screenshots).
- Duration is read correctly (`0:30` for the test clip) and the position/seek bar
  track playback.
- The play/pause button toggles state and icon.
- The build is clean (`go build` of both the fork packages and the demo).

**Not yet done / open items:**

1. **Not yet tested on a real Wayland session.** The dev box reports
   `XDG_SESSION_TYPE=x11`. The rendering path is windowing-system-agnostic, so it
   is expected to work unchanged on Wayland, but this should be confirmed on an
   actual Wayland compositor.
2. **Hardware decoding on Wayland/Intel** may require passing the native display
   to mpv via `MPV_RENDER_PARAM_WL_DISPLAY` (GLFW can supply the `wl_display`).
   Software decoding works everywhere as-is. Not yet wired.
3. **The `Video` widget lives in the demo.** Promoting it to a first-class,
   backend-neutral `fyne.io/fyne/v2/widget` would need a public home and a way to
   register a backend without a hard libmpv dependency in the toolkit.
4. **No audio-specific controls** (volume/mute) or track selection yet — mpv
   supports them; they would be more property wrappers like the existing ones.
5. **Error surfacing is minimal** — `RenderInto` silently returns on init error.
   A production widget would expose load/playback errors to the UI.

---

## 10. Glossary

- **FBO (Framebuffer Object)** — an off-screen render target in OpenGL. You can
  render into it and then use its attached texture elsewhere.
- **libmpv Render API** — mpv's headless rendering interface (`render.h` /
  `render_gl.h`): mpv draws frames into a target *you* provide (an FBO for the GL
  backend) instead of managing its own window.
- **`--wid` / window embedding** — the old X11 technique of telling a player to
  draw into another program's window by numeric ID. Not supported on Wayland;
  the reason a different approach was required.
- **VO (video output)** — mpv's output driver. `vo=libmpv` selects the embedding
  mode used by the Render API.
- **get_proc_address** — the callback through which mpv obtains OpenGL function
  pointers; we resolve them via GLFW so mpv shares Fyne's GL context.
- **cgo.Handle** — Go's mechanism for safely giving C a stable reference to a Go
  object across the cgo boundary.
- **Letterbox / pillarbox** — black (here: background) bars added top/bottom or
  left/right to fit a video of one aspect ratio into a box of another without
  stretching.
- **Painter / draw dispatch** — Fyne's GL renderer and its `drawObject` type
  switch that maps each canvas object type to a drawing routine.
