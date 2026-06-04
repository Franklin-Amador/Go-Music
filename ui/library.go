package ui

// library.go — Albums + Artists browsing tabs.
//
// Layout (left column when a music root is configured):
//
//   ┌─[Playlist] [Albums] [Artists]─┐
//   │                                │
//   │  (tab content)                 │
//   │                                │
//   └────────────────────────────────┘
//
// The Albums tab is a GridWrap of cover thumbnails; the Artists tab is a
// List of artist names. Clicking an artist filters the Albums tab to that
// artist's catalogue, so the navigation flow is:
//
//   Artists → click → (jumps to Albums, filtered) → click album → playback.

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gomusic/library"
)

// defaultCoverSVG is the placeholder rendered inside an album row when
// the album has no embedded art and no folder cover was found.
//
// Why pure shapes (no <text>, no alpha hex): Fyne's SVG renderer is a
// subset of full SVG. It chokes silently on hex-with-alpha fills like
// "#ffffffcc" (use fill-opacity instead), and on <text> with custom
// font-family. Building the music-note glyph from rect + circle
// primitives sidesteps both footguns and renders identically across
// every Fyne backend.
var defaultCoverSVG = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
  <rect width="100" height="100" rx="6" ry="6" fill="#26262c"/>
  <rect x="44" y="22" width="4" height="46" fill="#888"/>
  <rect x="78" y="14" width="4" height="46" fill="#888"/>
  <rect x="44" y="22" width="38" height="4" fill="#888"/>
  <ellipse cx="38" cy="68" rx="10" ry="8" fill="#888"/>
  <ellipse cx="72" cy="60" rx="10" ry="8" fill="#888"/>
</svg>`)

var defaultCoverResource = fyne.NewStaticResource("default-cover.svg", defaultCoverSVG)

const (
	// Thumb size used in the Albums list rows. Small enough that decoding
	// 1000s of covers stays cheap; large enough to recognize the artwork.
	albumThumbSize = float32(56)
)

// buildLibraryTabs wraps the existing playlist column into the first tab
// of an AppTabs and adds Albums + Artists tabs alongside. Called from
// buildUI once the playlist column is constructed.
//
// The tab container itself is stored on mainUI so library-scan callbacks
// (which arrive on background goroutines and dispatch via fyne.Do) can
// refresh the right widgets without crawling the scene graph.
func (ui *mainUI) buildLibraryTabs(playlistPanel fyne.CanvasObject) *container.AppTabs {
	ui.coverCache = make(map[*library.Album]fyne.Resource)

	playlistTab := container.NewTabItem("Playlist", playlistPanel)
	albumsTab := container.NewTabItem("Albums", ui.buildAlbumsPanel())
	artistsTab := container.NewTabItem("Artists", ui.buildArtistsPanel())
	songsTab := container.NewTabItem("Songs", ui.buildSongsPanel())

	t := container.NewAppTabs(playlistTab, albumsTab, artistsTab, songsTab)
	t.SetTabLocation(container.TabLocationTop)
	ui.libTabs = t
	return t
}

// buildAlbumsPanel returns the canvas for the Albums tab: a header row
// (status text + clear filter button) and a List of album rows beneath.
//
// We use widget.List (not GridWrap) for the same reason every other tab
// does: row recycling is rock-solid, layout is predictable, and the
// thumbnail+text pattern scales to thousands of entries with constant
// memory. Each row is an *albumRow with a small canvas.Image that always
// has a Resource (real cover or defaultCoverResource).
func (ui *mainUI) buildAlbumsPanel() fyne.CanvasObject {
	ui.albumList = widget.NewList(
		func() int { return len(ui.visibleAlbums) },
		// CreateItem: HBox(thumb, VBox(title, subtitle)) — same flat
		// pattern Artists/Songs use. NO wrapper struct: caching child
		// references in a struct breaks because widget.List recycles
		// objects in a way that leaves our stored pointers dangling
		// from the live render tree (the dreaded "rows have hover but
		// no visible content" symptom).
		func() fyne.CanvasObject {
			img := canvas.NewImageFromResource(defaultCoverResource)
			img.FillMode = canvas.ImageFillContain
			img.ScaleMode = canvas.ImageScaleSmooth
			img.SetMinSize(fyne.NewSize(albumThumbSize, albumThumbSize))

			title := canvas.NewText("", colorText)
			title.TextSize = 14
			title.TextStyle = fyne.TextStyle{Bold: true}

			subtitle := canvas.NewText("", colorTextDim)
			subtitle.TextSize = 11

			return container.NewHBox(img, container.NewVBox(title, subtitle))
		},
		// UpdateItem: extract children from the live container each
		// call — never trust cached references.
		func(i widget.ListItemID, obj fyne.CanvasObject) {
			if i < 0 || i >= len(ui.visibleAlbums) {
				return
			}
			album := ui.visibleAlbums[i]

			hb := obj.(*fyne.Container)
			img := hb.Objects[0].(*canvas.Image)
			vb := hb.Objects[1].(*fyne.Container)
			title := vb.Objects[0].(*canvas.Text)
			subtitle := vb.Objects[1].(*canvas.Text)

			if cover := ui.coverResource(album); cover != nil {
				img.Resource = cover
			} else {
				img.Resource = defaultCoverResource
			}
			img.Refresh()

			title.Text = album.Title
			title.Refresh()

			sub := album.Artist
			if album.Year > 0 {
				sub = fmt.Sprintf("%s · %d", album.Artist, album.Year)
			}
			subtitle.Text = sub
			subtitle.Refresh()
		},
	)
	ui.albumList.OnSelected = func(i widget.ListItemID) {
		if i < 0 || i >= len(ui.visibleAlbums) {
			return
		}
		album := ui.visibleAlbums[i]
		ui.albumList.UnselectAll()
		ui.confirmLoadAlbum(album)
	}

	ui.libStatus = canvas.NewText("", colorTextDim)
	ui.libStatus.TextSize = 12
	ui.libStatus.Alignment = fyne.TextAlignCenter

	ui.libClearFilter = widget.NewButton("× Clear filter", ui.clearArtistFilter)
	ui.libClearFilter.Importance = widget.LowImportance
	ui.libClearFilter.Hide()

	header := container.NewBorder(nil, nil, nil, ui.libClearFilter, container.NewPadded(ui.libStatus))

	return container.NewBorder(
		header,
		nil, nil, nil,
		ui.albumList,
	)
}

// buildSongsPanel returns the canvas for the Songs tab: a flat List of
// every track in the library, sorted by artist → album → track number.
// Clicking a track loads it directly into the playlist and starts playback.
func (ui *mainUI) buildSongsPanel() fyne.CanvasObject {
	ui.songList = widget.NewList(
		func() int { return len(ui.allTracks) },
		func() fyne.CanvasObject {
			title := canvas.NewText("", colorText)
			title.TextSize = 13
			title.TextStyle = fyne.TextStyle{Bold: true}
			sub := canvas.NewText("", colorTextDim)
			sub.TextSize = 10
			return container.NewVBox(title, sub)
		},
		func(i widget.ListItemID, obj fyne.CanvasObject) {
			if i < 0 || i >= len(ui.allTracks) {
				return
			}
			t := ui.allTracks[i]
			box := obj.(*fyne.Container)
			title := box.Objects[0].(*canvas.Text)
			sub := box.Objects[1].(*canvas.Text)
			title.Text = "  " + t.Title
			sub.Text = fmt.Sprintf("    %s", t.Artist)
			title.Refresh()
			sub.Refresh()
		},
	)
	ui.songList.OnSelected = func(i widget.ListItemID) {
		if i < 0 || i >= len(ui.allTracks) {
			return
		}
		track := ui.allTracks[i]
		ui.songList.UnselectAll()
		ui.eng.Stop()
		ui.eng.ClearPreload()
		ui.pl.Clear()
		ui.pl.Add(track.Path)
		ui.pl.SetCurrent(0)
		ui.applyFilter(ui.searchEntry.Text)
		ui.syncListSelection()
		if t := ui.pl.CurrentTrack(); t != nil {
			ui.loadAndPlay(t.Path)
		}
		if ui.libTabs != nil && len(ui.libTabs.Items) > 0 {
			ui.libTabs.SelectIndex(0)
		}
	}
	return ui.songList
}

// buildArtistsPanel returns the canvas for the Artists tab: a List of
// artist names with album count beneath each name.
func (ui *mainUI) buildArtistsPanel() fyne.CanvasObject {
	ui.artistList = widget.NewList(
		func() int {
			if ui.lib == nil {
				return 0
			}
			return len(ui.lib.Artists)
		},
		func() fyne.CanvasObject {
			name := canvas.NewText("", colorText)
			name.TextSize = 14
			name.TextStyle = fyne.TextStyle{Bold: true}
			sub := canvas.NewText("", colorTextDim)
			sub.TextSize = 10
			return container.NewVBox(name, sub)
		},
		func(i widget.ListItemID, obj fyne.CanvasObject) {
			if ui.lib == nil || i < 0 || i >= len(ui.lib.Artists) {
				return
			}
			a := ui.lib.Artists[i]
			box := obj.(*fyne.Container)
			name := box.Objects[0].(*canvas.Text)
			sub := box.Objects[1].(*canvas.Text)
			name.Text = "  " + a.Name
			sub.Text = fmt.Sprintf("    %d album%s", len(a.Albums), pluralS(len(a.Albums)))
			name.Refresh()
			sub.Refresh()
		},
	)
	ui.artistList.OnSelected = func(i widget.ListItemID) {
		if ui.lib == nil || i < 0 || i >= len(ui.lib.Artists) {
			return
		}
		artist := ui.lib.Artists[i]
		ui.artistList.UnselectAll()
		ui.filterByArtist(artist.Name)
		// Jump straight to the Albums tab — the artist row works as a
		// drill-down navigation cue.
		if ui.libTabs != nil && len(ui.libTabs.Items) >= 2 {
			ui.libTabs.SelectIndex(1)
		}
	}
	return ui.artistList
}

// refreshLibraryUI is called whenever the underlying ui.lib changes
// (scan finished, cache loaded, root cleared). It rebuilds visibleAlbums
// from the current artistFilter and pokes both list widgets to redraw.
func (ui *mainUI) refreshLibraryUI() {
	// Drop the cover cache — even if the same Album pointers persist,
	// after a rescan we want fresh resources picked up by Fyne's image
	// cache invalidation.
	ui.coverCache = make(map[*library.Album]fyne.Resource)

	ui.rebuildVisibleAlbums()
	ui.rebuildAllTracks()

	if ui.albumList != nil {
		ui.albumList.Refresh()
	}
	if ui.artistList != nil {
		ui.artistList.Refresh()
	}
	if ui.songList != nil {
		ui.songList.Refresh()
	}

	// Status line on the Albums tab.
	if ui.libStatus != nil {
		switch {
		case ui.lib == nil:
			ui.libStatus.Text = "No music root configured. Use File → Set Music Root… to scan a folder."
		case len(ui.lib.Albums) == 0:
			ui.libStatus.Text = "No audio files found in this folder."
		case ui.artistFilter != "":
			ui.libStatus.Text = fmt.Sprintf("Showing albums by %s", ui.artistFilter)
		default:
			ui.libStatus.Text = fmt.Sprintf("%d albums in library", len(ui.lib.Albums))
		}
		ui.libStatus.Refresh()
	}
	if ui.libClearFilter != nil {
		if ui.artistFilter != "" {
			ui.libClearFilter.Show()
		} else {
			ui.libClearFilter.Hide()
		}
	}
}

// rebuildVisibleAlbums rebuilds the slice the album-grid renders from,
// applying the artist filter if one is set. Called whenever lib or
// artistFilter changes.
func (ui *mainUI) rebuildVisibleAlbums() {
	if ui.lib == nil {
		ui.visibleAlbums = nil
		return
	}
	if ui.artistFilter == "" {
		ui.visibleAlbums = ui.lib.Albums
		return
	}
	ui.visibleAlbums = ui.visibleAlbums[:0]
	for _, a := range ui.lib.Albums {
		if a.Artist == ui.artistFilter {
			ui.visibleAlbums = append(ui.visibleAlbums, a)
		}
	}
}

// rebuildAllTracks flattens every track in the library into allTracks,
// ordered by album artist → album year/title → track number.
func (ui *mainUI) rebuildAllTracks() {
	if ui.lib == nil {
		ui.allTracks = nil
		return
	}
	ui.allTracks = nil
	for _, album := range ui.lib.Albums {
		ui.allTracks = append(ui.allTracks, album.Tracks...)
	}
}

// filterByArtist sets the album-grid scope and refreshes.
func (ui *mainUI) filterByArtist(name string) {
	ui.artistFilter = name
	ui.refreshLibraryUI()
}

// clearArtistFilter restores the full album grid.
func (ui *mainUI) clearArtistFilter() {
	ui.artistFilter = ""
	ui.refreshLibraryUI()
}

// confirmLoadAlbum replaces the current playlist with the contents of
// the chosen album and starts playback from its first track. Acts as the
// "Replace + Play" interaction selected during the design discussion.
func (ui *mainUI) confirmLoadAlbum(album *library.Album) {
	if album == nil || len(album.Tracks) == 0 {
		return
	}
	ui.eng.Stop()
	ui.eng.ClearPreload()
	ui.pl.Clear()
	for _, t := range album.Tracks {
		ui.pl.Add(t.Path)
	}
	if ui.pl.Len() == 0 {
		// Every track was unsupported by the playlist filter; nothing to do.
		dialog.ShowInformation("Album not playable",
			"None of this album's tracks are in a supported format.", ui.win)
		return
	}
	ui.pl.SetCurrent(0)
	ui.applyFilter(ui.searchEntry.Text)
	ui.syncListSelection()
	if t := ui.pl.CurrentTrack(); t != nil {
		ui.loadAndPlay(t.Path)
	}
	// Jump to Playlist tab so the user sees the queue they just loaded.
	if ui.libTabs != nil && len(ui.libTabs.Items) > 0 {
		ui.libTabs.SelectIndex(0)
	}
}

// coverResource memoises *fyne.StaticResource per album so the canvas.Image
// inside each cell can re-bind without re-decoding the JPEG bytes on every
// scroll tick. Falls through to nil for tag-less files; the cell renders
// a glyph placeholder in that case.
func (ui *mainUI) coverResource(album *library.Album) fyne.Resource {
	if album == nil || len(album.CoverData) == 0 {
		return nil
	}
	if r, ok := ui.coverCache[album]; ok {
		return r
	}
	r := fyne.NewStaticResource("cover_"+album.Artist+"_"+album.Title, album.CoverData)
	ui.coverCache[album] = r
	return r
}

// (No albumRow struct: the Albums tab inlines its row factory inside
// widget.NewList. Caching child references in a wrapper struct breaks
// row recycling — see the comment in buildAlbumsPanel.)

// ── Library actions wired to the menu ───────────────────────────────────────

// setMusicRoot stores the chosen folder, kicks off a background scan, and
// shows progress in a modal dialog. Called from the File menu.
func (ui *mainUI) setMusicRoot(path string) {
	ui.cfg.MusicRoot = path
	ui.cfg.save()
	ui.scanLibrary(path, true)
}

// scanLibrary runs a Library.Scan in a goroutine and shows a small modal
// dialog with a progress bar. fromUI=true means we surface a Done message
// when it completes; from startup-cache rebuilds we skip the popup so the
// window doesn't open with a modal.
func (ui *mainUI) scanLibrary(root string, fromUI bool) {
	progressLabel := widget.NewLabel("Scanning library…")
	progressBar := widget.NewProgressBar()
	dlg := dialog.NewCustomWithoutButtons("Scanning",
		container.NewVBox(progressLabel, progressBar), ui.win)
	dlg.Resize(fyne.NewSize(360, 120))
	if fromUI {
		dlg.Show()
	}

	go func() {
		lib := library.New()
		err := lib.Scan(root, func(done, total int) {
			fyne.Do(func() {
				if total <= 0 {
					return
				}
				progressBar.SetValue(float64(done) / float64(total))
				progressLabel.SetText(fmt.Sprintf("Scanning library… (%d / %d)", done, total))
			})
		})
		fyne.Do(func() {
			if fromUI {
				dlg.Hide()
			}
			if err != nil {
				dialog.ShowError(err, ui.win)
				return
			}
			ui.lib = lib
			ui.artistFilter = ""
			ui.refreshLibraryUI()
			// Persist the cache next to the config file so subsequent
			// launches load instantly.
			if dir := configDir(); dir != "" {
				_ = lib.Save(library.CachePath(dir))
			}
			if fromUI {
				dialog.ShowInformation("Library ready",
					fmt.Sprintf("Indexed %d albums from %d artists.", len(lib.Albums), len(lib.Artists)),
					ui.win)
			}
		})
	}()
}

// loadCachedLibrary attempts to deserialize a previously saved library
// index. Called once at startup; missing/corrupt caches just leave ui.lib
// nil and let refreshLibraryUI render the "no library yet" placeholder.
func (ui *mainUI) loadCachedLibrary() {
	dir := configDir()
	if dir == "" {
		return
	}
	lib, err := library.Load(library.CachePath(dir))
	if err != nil {
		return
	}
	ui.lib = lib
}

// ── small helpers ────────────────────────────────────────────────────────────

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func truncateForCell(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

