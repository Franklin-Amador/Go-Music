//go:build !windows

package mediakeys

// Cross-platform stub. Real implementation lives in mediakeys_windows.go.
// On other OSes the media-key plumbing is a no-op so the rest of the app
// builds cleanly without touching them.

// Handler is the contract the caller passes to Start. Any of the fields may
// be nil; missing handlers are simply ignored when the corresponding key fires.
type Handler struct {
	OnPlayPause func()
	OnNext      func()
	OnPrev      func()
	OnStop      func()
}

// Start is a no-op on non-Windows platforms.
func Start(_ Handler) {}
