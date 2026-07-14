# fyne-mpv-video

A working demo of **video playback inside a [Fyne](https://fyne.io) window**,
including on **Wayland** — by embedding [libmpv](https://mpv.io) through its
OpenGL Render API instead of the classic (Wayland-incompatible) child-window
`--wid` embedding.

The video is rendered by mpv into an offscreen OpenGL framebuffer that Fyne's
GL painter then composites like any other texture, so the same code works on
X11, Wayland, macOS and Windows.

This demo drives the `canvas.GLVideo` primitive proposed for Fyne upstream
(see [fyne-io/fyne#449](https://github.com/fyne-io/fyne/issues/449)). Until that
lands, `go.mod` uses a `replace` directive pointing at the fork that carries it.

See [DOCUMENTATION.md](DOCUMENTATION.md) for a complete, top-to-bottom
explanation of the approach, the frame flow, and every file.

## Requirements

- Go 1.22+
- libmpv development headers (`libmpv-dev` / `media-video/mpv` with `libmpv` USE)
- A checkout of the Fyne fork carrying `canvas.GLVideo`, next to this repo
  (the `replace fyne.io/fyne/v2 => ../fyne` in `go.mod`)

## Build & run

Desktop OpenGL (X11 or XWayland):

```sh
go build
./fyne-mpv-video test.mp4
```

Native Wayland with EGL/GLES only (e.g. a libglvnd built without X):

```sh
go build -tags "wayland egl gles gles2"
./fyne-mpv-video test.mp4
```

You can pass any local file or URL that mpv can open.
