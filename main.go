package main

import (
	"log"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <video-file-or-url>", os.Args[0])
	}
	file := os.Args[1]

	player, err := newMPVPlayer(file)
	if err != nil {
		log.Fatalf("failed to start mpv: %v", err)
	}

	a := app.NewWithID("io.fyne.mpvdemo")
	w := a.NewWindow("Fyne + mpv (Wayland-friendly)")

	video := NewVideo(player)
	w.SetCloseIntercept(func() {
		video.Close()
		w.Close()
	})

	w.SetContent(video)
	w.Resize(fyne.NewSize(800, 520))
	w.ShowAndRun()
}
