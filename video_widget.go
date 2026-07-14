package main

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// videoController is the playback surface the Video widget drives. mpvPlayer
// satisfies it, but the widget stays decoupled from libmpv specifics.
type videoController interface {
	canvas.GLVideoRenderer
	SetOnUpdate(func())
	Play()
	Pause()
	TogglePause() bool
	IsPaused() bool
	Position() float64
	Duration() float64
	SeekTo(float64)
	Close()
}

// Video is a reusable widget that plays a video with a play/pause button and a
// seek bar. The frame is rendered by a videoController (e.g. libmpv via the
// OpenGL Render API) into a canvas.GLVideo, so it works without any native
// window embedding - including on Wayland.
type Video struct {
	widget.BaseWidget

	player  videoController
	video   *canvas.GLVideo
	playBtn *widget.Button
	seek    *widget.Slider
	timeLbl *widget.Label

	stop     chan struct{}
	seeking  bool // true while the user drags the slider, to suppress feedback
}

// NewVideo returns a Video widget backed by the given controller.
func NewVideo(player videoController) *Video {
	v := &Video{player: player, stop: make(chan struct{})}
	v.ExtendBaseWidget(v)

	v.video = canvas.NewGLVideo(player)
	v.video.SetMinSize(fyne.NewSize(320, 180))

	v.playBtn = widget.NewButtonWithIcon("", theme.MediaPauseIcon(), v.togglePlay)
	v.timeLbl = widget.NewLabel("0:00 / 0:00")

	v.seek = widget.NewSlider(0, 1)
	v.seek.Step = 0.001
	v.seek.OnChangeEnded = func(val float64) {
		if dur := v.player.Duration(); dur > 0 {
			v.player.SeekTo(val * dur)
		}
		v.seeking = false
	}
	v.seek.OnChanged = func(float64) { v.seeking = true }

	// Repaint the frame whenever mpv produces a new one.
	player.SetOnUpdate(func() { fyne.Do(v.video.Refresh) })

	go v.tick()
	return v
}

func (v *Video) togglePlay() {
	paused := v.player.TogglePause()
	if paused {
		v.playBtn.SetIcon(theme.MediaPlayIcon())
	} else {
		v.playBtn.SetIcon(theme.MediaPauseIcon())
	}
}

// tick refreshes the seek bar and time label roughly twice a second.
func (v *Video) tick() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
			pos, dur := v.player.Position(), v.player.Duration()
			fyne.Do(func() {
				v.timeLbl.SetText(formatTime(pos) + " / " + formatTime(dur))
				if dur > 0 && !v.seeking {
					v.seek.SetValue(pos / dur)
				}
			})
		}
	}
}

// Close stops the update loop and releases the player.
func (v *Video) Close() {
	select {
	case <-v.stop:
	default:
		close(v.stop)
	}
	v.player.Close()
}

func (v *Video) CreateRenderer() fyne.WidgetRenderer {
	controls := container.NewBorder(nil, nil, v.playBtn, v.timeLbl, v.seek)
	content := container.NewBorder(nil, controls, nil, nil, v.video)
	return widget.NewSimpleRenderer(content)
}

func formatTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds)
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
