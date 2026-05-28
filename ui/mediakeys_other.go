//go:build !windows

package ui

// Cross-platform stub. Real implementation lives in mediakeys_windows.go.
// On other OSes the media-key plumbing is a no-op so the rest of the app
// builds cleanly without touching them.

type mediaKeysHandler struct {
	OnPlayPause func()
	OnNext      func()
	OnPrev      func()
	OnStop      func()
}

func startMediaKeys(_ mediaKeysHandler) {}
