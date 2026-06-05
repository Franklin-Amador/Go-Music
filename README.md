# Go Music

A high-resolution audio player written in Go, built around clean DSD/DSF playback.

The project replaces an earlier Python implementation that suffered from GIL/GC interference in the audio callback path — Go's goroutines and deterministic memory model give a stable real-time pipeline that DSD demands.

## Status

Working on Windows. The audio engine, the playback chain, and the UI are all in production shape.

| Format | Status                                              |
| ------ | --------------------------------------------------- |
| DSF    | ✅ Single-stage Kaiser FIR decimation, audiophile-grade |
| DFF    | ✅ Same pipeline as DSF                             |
| FLAC   | ✅ All standard rates; embedded picture/lyrics      |
| WAV    | ✅ 16/24/32-bit PCM and float                       |
| MP3    | ✅ via go-mp3; full ID3v2 tag/picture/lyrics support |

## Quick start

Requirements: Go 1.21+, GCC (for the Fyne UI + miniaudio cgo — see _Build_ below).

```powershell
go build -ldflags="-H windowsgui -s -w" -o gomusic.exe .
.\gomusic.exe
```

The `-H windowsgui` flag makes the binary a Windows GUI subsystem app, so no
console window appears next to the player. `-s -w` strips debug symbols and
roughly halves the binary size.

`File → Open Folder…` to add a directory of music (recursively — subfolders like `Deezer/`, album folders, etc. are walked). Or drag-and-drop files / folders straight onto the window. Click any track to play. Tracks auto-advance one after another.

For album / artist browsing, use `File → Set Music Root…` to point at the top of your collection. The library is scanned in the background, indexed by `(album-artist, album)`, and cached to disk (`%APPDATA%\gomusic\library.gob`) so subsequent launches load instantly. `File → Rescan Library` refreshes the index after adding new music.

## Features

### Audio quality

- **Bit-perfect WASAPI exclusive mode** via `malgo` / miniaudio. The engine tries exclusive first; on success the 176.4 kHz output goes straight to the DAC with no Windows mixer in the path. Falls back to oto shared mode automatically if the device rejects exclusive or another app holds it. User-controllable from `Edit → Exclusive mode: on/off` — turn it off when you want YouTube/Discord/system sounds to mix alongside.
- **Output-mode indicator in the UI.** A small `EXCLUSIVE` / `SHARED` label sits in the progress row (next to the elapsed-time counter) so the user can tell at a glance whether they're getting bit-perfect output or routed through the Windows mixer — no need to read the log to find out. The label follows the current track's accent color when exclusive, dim gray when shared.
- **Bit-clean DSD pipeline.** Single-stage decimating Kaiser FIR at the DSD bit rate — no second resampling stage, no audible alias noise. See [_DSD path_](#dsd-path-the-audiophile-path).
- **High-quality PCM resampling.** Polyphase Kaiser-sinc resampler, parallelized across all CPU cores.
- **Hearing-safety chain.** Per-channel DC blocker, soft-knee limiter, and a 25 kHz brick-wall reconstruction filter for DSD. See [_Safety_](#safety-hearing-and-dac-protection).
- **Crossfade between PCM tracks.** Configurable in `Edit → Crossfade: Ns / off`, default 4 s. The next track is preloaded in the background ~10 s before the current one ends, then mixed in with a linear gain ramp directly inside `pcmReader.Read` — no second player, no second device handle, no gap. DSD tracks bypass crossfade and hard-cut as before. See [_Crossfade_](#crossfade).

### Playback & metadata

- **All-format metadata.** Title / artist / album / album-artist / year / track number / album art / lyrics extracted from FLAC, MP3, WAV, and DSF (via ID3v2 at the DSF metadata offset).
- **Synced lyrics with depth-of-field animation.** Local `<song>.lrc` → embedded tags → [lrclib.net](https://lrclib.net) (cached on disk). LRCLIB matching prefers the variant whose track duration is closest to the file's, so remasters/singles/album cuts don't deliver out-of-sync lyrics. The active line eases dramatically into focus (14 → 22 px, bold, accent-coloured), the lines immediately above and below take an intermediate size (16 px white), and everything else stays at the relaxed idle size — an Apple-Music-style focus effect that reads cleanly even from across the room. If the fetch comes back empty (offline, LRCLIB miss), a **Retry lookup** button appears so the user can try again without re-loading the track.
- **Latency-compensated playback position.** Position reporting subtracts the player's `BufferedSize()` so synced lyrics align with what the DAC is actually emitting, not what's been handed downstream. In exclusive mode the buffer is ~5 ms so this is barely measurable; in shared mode it's ~200 ms and the compensation is what keeps the lyrics frame-accurate.
- **Animated waveform visualizer.** A continuous mirrored peak-envelope line drawn under the album art, with a soft glow halo and temporal smoothing — pulls the latest ~38 ms of audio from the visualizer ring at 30 fps. Tap or click on it to seek. See [_Visualizer_](#visualizer).
- **Seek for both PCM and DSD.** Click or drag the progress bar OR click anywhere on the waveform. PCM jumps the in-memory read position instantly; DSD restarts the decoder at the closest block boundary to the target time (under one second in practice).
- **Shuffle + repeat.** Shuffle generates a fresh Fisher–Yates permutation each time you toggle it on, with the current track pinned at the head so playback continues uninterrupted. Repeat replays the current track when it ends. State is visible as a small white dot under each toggle button.

### Library view

- **Albums + Artists tabs** alongside the Playlist tab in the left column.
- **Album grid** with cover thumbnails (140×140) + title + artist. Click an album → replaces the current playlist with its tracks (track-number ordered) and starts playback from track 1. The cell fallback is a dark rectangle with a large ♪ glyph when no cover is found.
- **Artist list** — click an artist to filter the album grid down to their catalogue. A `× Clear filter` button restores the full grid.
- **Cover detection** uses embedded art first, then falls back to `cover.jpg` / `folder.jpg` / `album.jpg` / `front.jpg` (and their `.jpeg` / `.png` variants) inside the album's folder — the standard Foobar / MusicBee / Roon layout.
- **Index persistence** via `gob` to `%APPDATA%\gomusic\library.gob`, versioned (`Version: 1`) so future schema bumps can migrate.

### Input

- **Keyboard shortcuts.** Space = play/pause, ←/→ = seek ±5 s, ↑/↓ = volume ±5 %, Ctrl+←/→ = previous/next track, **S** = shuffle, **R** = repeat, **L** = lyrics panel, **Ctrl+F** = search playlist, **Del** = remove current track from playlist, **Ctrl+Shift+↑/↓** = move current track up/down without interrupting playback, **Esc** = close search bar / clear filter.
- **System media keys** (Play/Pause / Next / Previous / Stop). Registered via Win32 `RegisterHotKey` on a dedicated message-pump goroutine; the keys work even when the app doesn't have focus, and most Bluetooth headphones' transport buttons map to them too.
- **Drag-and-drop** files or folders directly onto the window.
- **Playlist search** (Ctrl+F) — live filter on track titles. Filtering is a pure index remap; reordering and Current tracking still operate on the canonical list regardless of filter state.

### State

- **Persisted between sessions** (saved on window close to `%APPDATA%\gomusic\config.json`): volume, playlist (full path list + current index), lyrics-panel visibility, crossfade duration, exclusive-mode preference, music root for the library view.

### Look

- **Responsive UI.** Album art uses a custom square-fit layout (`squareCenterLayout` in `ui/window.go`) so the cover grows to the largest square that fits inside the player column, maintaining aspect ratio as the window resizes. Playlist / library tabs + collapsible lyrics panel sit beside it.
- **Album-derived dynamic accent.** When a track loads, the dominant hue of its album art is extracted (saturation-weighted histogram on a 64×64 sample grid — see `ui/dominant.go`) and applied to the quality label, the `EXCLUSIVE` indicator, the waveform glow, and the active lyrics line. A subtle vertical gradient of the same hue at ~14 % alpha sits behind the player column as an ambient backdrop. The whole UI quietly "vibrates with" the current track instead of being monochromatic.
- **Playlist auto-tracks the player.** The visible selection in the playlist follows whichever song is actually playing — when a track auto-advances, when the user hits next/prev, or when a crossfade transitions to the preloaded next track. No more clicking around to figure out what's on.
- **Custom dark theme** with a Spotify-ish accent green (used as the fallback when no album art is present) and tuned typography (28 px bold title, 16 px artist line, generous padding).

## Architecture

```
┌──────────────────┐    ┌──────────────────────────────┐    ┌─────────────┐
│  Source decoder  │───▶│  Producer goroutine          │───▶│ Ring buffer │
│  (DSD / FLAC /   │    │  decimate / resample /       │    │ (SPSC,      │
│   WAV / MP3)     │    │  DC-block / soft-limit /     │    │  lock-free) │
└──────────────────┘    │  push to visualizer          │    └──────┬──────┘
                        └──────────────────────────────┘           │
                                                                   ▼
                              ┌───────────────────────────────────────────┐
                              │ audioPlayer interface — one of:           │
                              │                                           │
                              │  • malgoPlayer (WASAPI EXCLUSIVE, ~5 ms)  │
                              │    bit-perfect, silences other apps       │
                              │                                           │
                              │  • oto.Player (WASAPI SHARED, ~200 ms)    │
                              │    plays alongside YouTube/Discord, but   │
                              │    Windows resamples + mixes              │
                              │                                           │
                              │ Both pull from the same pcmReader /        │
                              │ dsdStreamReader — the rest of the engine  │
                              │ doesn't know which backend is active.     │
                              └───────────────────────────────────────────┘
```

Backend selection happens in `Engine.openPlayer(io.Reader)`:

1. If the user has explicitly turned exclusive mode off (`Edit → Exclusive mode: off`, persisted in config), skip malgo entirely and open an oto shared-mode player. Other apps keep playing.
2. Otherwise try `newExclusivePlayer` first — if the DAC accepts 176.4 kHz / float32 stereo exclusive and no other app holds the device, this returns and the engine plays bit-perfect.
3. If exclusive fails for any reason, fall back to oto. The user never sees a popup; the log indicates which backend won (`audio: WASAPI exclusive mode active` vs `audio: WASAPI shared mode (...)`).

Both backends are wrapped in the `audioPlayer` interface (Play / Pause / IsPlaying / BufferedSize / Close) so the rest of the engine — `pcmReader`, `dsdStreamReader`, `watchPlayerDone`, `Position()` — is identical regardless of which one is active. Volume, DSP, crossfade mixing, and the safety chain all live in the producer / reader; the backend just consumes a `io.Reader`.

### DSD path (the audiophile path)

Output rate is fixed at **176 400 Hz** stereo. Every standard DSD rate divides this by an integer (DSD64 ÷ 16, DSD128 ÷ 32, DSD256 ÷ 64, DSD512 ÷ 128) so the pipeline can be a **single anti-aliasing stage**:

```
DSD bits  ─►  long Kaiser FIR (decimating, runs at fs_DSD)  ─►  PCM @ 176.4 kHz
```

Filter design:

- **Cutoff:** 25 kHz (preserves all human-audible content; cuts the steep DSD noise-shaping spectrum that begins above ~20 kHz).
- **Stopband:** outputRate / 2 .. fs_DSD / 2, attenuation **−115 dB** (Kaiser β = 12).
- **Length scales with DSD rate** so the transition (25 kHz → 88.2 kHz) always fits the Kaiser rule:

  | Source | FIR length | Per-channel cost |
  | ------ | ---------- | ---------------- |
  | DSD64  | ~340 taps  | ~60 MOPS         |
  | DSD128 | ~670 taps  | ~120 MOPS        |
  | DSD256 | ~1340 taps | ~240 MOPS        |

Channels are processed in parallel goroutines.

Why this matters: the previous design (block-mean decimation followed by a post-decimation Kaiser FIR) had a **rectangular pre-decimation filter with only ~−18 dB stopband** in the worst alias bands (around 200 kHz, 380 kHz, …). DSD shaped-noise at those frequencies aliased back into the audible passband as a constant high-volume buzz. The single long FIR achieves ~−115 dB everywhere it matters, so aliased noise stays below the noise floor of any real DAC.

### PCM path

PCM is decoded fully into memory at native rate, then resampled to 176 400 Hz with a **polyphase Kaiser-sinc resampler** (16 half-taps, β = 8.6, ~70 dB stopband). For clean integer ratios (44.1 → 176.4 = ×4) this is fast; for ugly ratios (96 → 176.4 = ×147/80) it takes a couple of seconds at load time. The resampler is **parallelized across all CPU cores** — each worker processes a disjoint output range against the shared (read-only) source buffer.

Typical load times on a 12-core CPU:

| Source              | Load time |
| ------------------- | --------- |
| FLAC 44 kHz (4 min) | ~3 s      |
| FLAC 96 kHz (4 min) | ~6 s      |
| MP3 44 kHz (4 min)  | ~1 s      |
| DSF (header only)   | <50 ms    |

### Ring buffer

A lock-free single-producer / single-consumer ring of `float32` samples. The producer writes processed audio; the audio callback memcpy's into oto's buffer with **zero DSP work**. This is what keeps DSD playback drop-free.

### Safety (hearing and DAC protection)

Three defensive stages, always active, run after the volume control and before the ring buffer:

1. **DSD reconstruction cutoff at 25 kHz.** Stops the steep noise-shaping spectrum of DSD from reaching the DAC. Ultrasonic energy is inaudible but at high volume can damage tweeters and hearing without warning.
2. **DC blocker** — single-pole high-pass at ~5 Hz per channel. A sustained DC offset off-centers a speaker's voice coil and overheats it. This guarantees we never emit one.
3. **Soft limiter** — smooth tanh-style knee on samples whose magnitude exceeds 0.95, compressing toward a ±0.99 ceiling. Hard clipping creates square-wave edges with enormous ultrasonic harmonics — harsh to the ear, dangerous to tweeters. This stage guarantees the output never clips, no matter what the source content, volume control, or future EQ does.

These work together: no DC, no clipping, no inaudible-but-harmful ultrasonics. The chain is hard-coded — not configurable — because there is no legitimate audiophile use case for disabling any of them.

### Lyrics

Three-step fallback chain, runs asynchronously after a track loads (never blocks playback):

1. **Local `<song>.lrc` file** next to the audio file — standard karaoke format, synced. The `[offset:±N]` tag is honored (positive offset shifts every line earlier).
2. **Embedded tags** inside the file — USLT for MP3 / DSF (via ID3v2 at the DSF metadata offset), LYRICS for FLAC.
3. **[lrclib.net](https://lrclib.net) API** — free, no auth, huge coverage. Matching is duration-aware: `/api/get` is tried first, and if the returned record's duration deviates more than 2.5 s from ours, `/api/search` is queried and the closest-duration record with `syncedLyrics` is selected. This avoids the common case where a remaster/single/album-cut record matches by name but the LRC was synced to a different release. Results (positive and negative) are cached on disk under `%LocalAppData%\gomusic\lyrics\`. The cache key embeds a version tag (currently `v5`) inside the SHA-1 hash, so bumping the matching logic auto-invalidates every existing entry without leaving orphan subdirectories behind. A negative result (no lyrics found) caches the empty string so we don't re-hit LRCLIB on every replay — and the UI exposes a **Retry lookup** button next to the "No lyrics found" message that clears that specific cache entry and re-queries, so the user can recover after going from offline back to online without re-loading the track.

The UI shows lyrics in a right-side collapsible panel with a depth-of-field effect:

- Active line: **22 px bold**, coloured with the current track's accent (extracted from the cover) — the focal point.
- Adjacent lines (±1 of the active): **16 px white** — a soft transition zone so the size change isn't a hard step.
- Everything else: **14 px dimmed gray** — relaxed, out-of-focus context.

Transitions ease over ~200 ms (ease-out) and the viewport glides smoothly (~420 ms, ease-in-out) to keep the active line centred. In-flight animations are stopped when a new line change arrives, so rapid transitions stay continuous without piling up.

For plain lyrics, the whole text is shown statically.

### Bit-perfect (now available)

WASAPI exclusive mode is the default. When the DAC accepts the format and no other process holds it, the engine opens the device exclusively via `malgo` / miniaudio and the 176.4 kHz / float32 stereo stream goes straight to the DAC — no Windows mixer, no resampling, no system-volume curve, ~5 ms hardware buffer. Bit-perfect end-to-end.

When you DON'T want that (background YouTube, Discord calls, system sounds), flip `Edit → Exclusive mode: off`. The engine then opens an `oto v3` shared-mode player on the next track: ~200 ms of buffer, Windows resamples to the device's negotiated rate, multiple apps coexist. The setting persists in config.

Even in exclusive mode the safety chain (DC blocker + soft limiter) still touches every sample. That's strictly speaking not bit-perfect by definition — but the limiter only acts on samples whose magnitude exceeds 0.95, and most music never triggers it. The DC blocker is a single-pole HPF at ~5 Hz so its effect on audible content is unmeasurable.

### Crossfade

PCM-only, configurable in `Edit → Crossfade`. Default duration is 4 seconds.

The trigger comes from the UI: `OnPositionChange` fires 4×/s and checks `remaining < crossfadeSec + 6 s`. If yes and there's a next track in the playlist, it calls `Engine.Preload(nextPath)`. The engine decodes that track into a parked "next" slot (separate `pcmData` / `pcmFrames` / `FileInfo`) in a background goroutine, without touching playback.

When `pcmReader.Read` notices the current track has fewer frames left than `crossfadeSec * sampleRate` AND a preload is sitting in the next slot, it calls `Engine.tryStartCrossfade`:

- Moves the current track + its read position into the "outgoing" slot
- Promotes the preloaded next into the active slot (`pcmData = next`, `pcmPos = 0`)
- Updates `Engine.Info` to the new track
- Fires `OnTrackTransition(newInfo)` from the audio thread

The UI handler wraps with `fyne.Do`, advances `pl.SetCurrent` to keep the playlist cursor honest, and refreshes title / cover / lyrics / waveform position WITHOUT calling `Load` — the engine has already done the swap internally.

From there `pcmReader.Read` runs in **mix mode**: for each frame it computes `gainOut = remainingOut / fadeTotal` (linear ramp 1→0) and `gainCur = 1 - gainOut`, mixes current+outgoing per sample, applies volume, writes to the output buffer, and pushes to the visualizer. When `outgoingRemaining` reaches zero, the outgoing slot is released back to the GC and the reader returns to the original single-track fast path — zero overhead from that point on.

Edge cases handled:

- **User picks a different track manually during a fade** → `loadAndPlay` calls `Engine.ClearPreload`; `Load` resets pcmData; outgoing slot is dropped.
- **User seeks during a fade** → `Engine.Seek` (PCM path) clears the outgoing slot; the leftover audio from the previous track would no longer make musical sense.
- **Repeat-track mode is on** → the UI never calls `Preload`; auto-advance hard-cuts the same track.
- **Current track is DSD** → `Engine.Preload` no-ops; DSD tracks always hard-cut.
- **Next track in the playlist is DSD** → same, no-op.
- **The current track is shorter than the configured fade window** → the fade collapses to whatever frames remain so the gain ramp doesn't overshoot.
- **Crossfade toggled to 0 (off)** → `pcmReader.Read` runs its original single-track fast path with zero mixing overhead, identical to pre-crossfade behaviour.

### Visualizer

A continuous, mirrored peak-envelope waveform drawn under the album art. Pipeline:

- The producer pushes every post-safety-chain sample chunk into a lock-free ring inside `audio.Visualizer` — pure memcpy, safe even from the audio reader thread.
- The UI animation loop ticks at 30 Hz. Each tick it calls `Visualizer.Waveform(96, 38)`, which pulls the last 38 ms of audio and reduces it to 96 peak-amplitude points (mono-summed, `max(|sample|)` per time slice).
- The `waveformWidget` draws those points as two mirrored polylines (top + bottom) around a faint horizontal axis. Each segment renders twice: a thick translucent stroke for the glow halo, a thin bright stroke on top — a soft accent-green halo without any shader machinery.
- **Edge fade** at the outer ~10 % of points so the curve dissolves into the background instead of cutting hard against the container.
- **Temporal smoothing**: each frame, the displayed envelope eases 35 % of the way toward the latest sample — the line flows instead of snapping. When playback stops, the envelope eases back to flat on its own.
- All canvas objects (axis line, 95 glow lines × 2, 95 sharp lines × 2) are allocated once at construction; per-frame work is pure mutation of `Position1`/`Position2`.

The FFT-band spectrum machinery is still in `audio/visualizer.go` (`Snapshot()`) for future reuse, but the UI no longer drives it.

## Build

> **Two frontends coexist right now.**
> `main` carries the **Fyne UI** (`gomusic.exe`).
> Branch `webui-migration` builds the **Wails + Svelte 5 UI** (`gomusic-web.exe`) — same audio engine, richer visual layer.
> The migration is in progress; do not merge to main until feature parity is confirmed.

### Fyne UI (`gomusic.exe`)

The Fyne UI requires cgo and a working GCC. On Windows install MinGW-w64 with a Go-compatible version (avoid GCC ≥ 16; Go's cgo can't parse those object files). The easiest route:

```powershell
choco install mingw -y     # in an Administrator shell
```

This installs MinGW 15.x with GCC 14.x, which works with Go 1.25.

Then:

```powershell
go build -ldflags="-H windowsgui -s -w" -o gomusic.exe .
```

### Running on another PC (especially Windows 10)

The MinGW-w64 15.x toolchain links against the **Universal C Runtime (UCRT)**, so the resulting `gomusic.exe` imports `api-ms-win-crt-*.dll`. These ship with Windows 11 and recent Windows 10 builds (≥ 1809) but are missing from older Win10 installs and the .exe will silently fail to launch there.

Fix on the target machine: install the **Microsoft Visual C++ Redistributable 2015-2022 (x64)** — it bundles UCRT and is the same redist most installers ship today.

```
https://aka.ms/vs/17/release/vc_redist.x64.exe
```

A one-time install is enough. After a reboot the same `gomusic.exe` runs without rebuilding.

(For fully self-contained distribution, switch the toolchain to the MSVCRT-based MinGW-w64 in MSYS2 — `pacman -S mingw-w64-x86_64-toolchain` — which produces a .exe that depends only on DLLs present in every Windows since XP. Requires no UCRT install on the target. Not the default here because the Chocolatey-installed MinGW is the path of least friction for the developer machine.)

### Wails UI (`gomusic-web.exe`) — branch `webui-migration`

Requires the [Wails CLI v2](https://wails.io) and WebView2 Runtime (bundled with Windows 11; download the bootstrapper for Windows 10).

```powershell
# One-time CLI install
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Build (from the webui/ subdirectory)
cd webui
wails build          # → build/bin/gomusic-web.exe

# Hot-reload dev mode
wails dev
```

The same MinGW GCC used for the Fyne build is sufficient — no extra toolchain.
WebView2 must be installed on the target machine (present on all Win11 machines
by default; [download here](https://go.microsoft.com/fwlink/p/?LinkId=2124703) for Win10).

### Icon (Fyne binary)

The Explorer / Start-menu `.exe` icon is embedded via `resource_windows_amd64.syso` (generated once with `rsrc -ico ui/go_music_icon_1.ico -arch amd64 -o resource_windows_amd64.syso`).

The in-app window/title-bar icon is a separate **PNG** (`ui/go_music_icon_1.png`), `//go:embed`-ed and applied via `app.SetIcon` / `window.SetIcon`. Fyne renders icons via Go's `image.Decode`, which has no `.ico` support in stdlib — passing `.ico` bytes paints a black rectangle. The PNG is generated once from the `.ico` with PowerShell:

```powershell
Add-Type -AssemblyName System.Drawing
$ico = New-Object System.Drawing.Icon("ui\go_music_icon_1.ico", 256, 256)
$ico.ToBitmap().Save("ui\go_music_icon_1.png", [System.Drawing.Imaging.ImageFormat]::Png)
```

## Project layout

```
audio/
  dsd.go              DSF/DFF parsers, DSDDecoder iterator, SeekTo for DSD seek
  dsd_decimator.go    Long Kaiser FIR — single-stage DSD→PCM (channel-parallel)
  engine.go           Engine, public API, dual-backend openPlayer (malgo / oto),
                      audioPlayer interface, producer goroutines, Seek() (PCM:
                      pcmPos.Store + clear outgoing crossfade slot; DSD: full
                      producer restart at block-aligned offset via
                      dsdSeekBytes/dsdSeekTime), Position() with BufferedSize()
                      compensation, crossfade (Preload / tryStartCrossfade /
                      outgoing-slot mixing in pcmReader.Read),
                      SetExclusiveMode / SetCrossfade atomics for cheap
                      audio-thread loads, OnTrackTransition callback
  exclusive.go        malgoPlayer — WASAPI exclusive backend via gen2brain/malgo
                      (miniaudio cgo binding). Implements the audioPlayer
                      interface so the engine treats it identically to oto.
                      dataCallback pulls from the same pcmReader / dsdStreamReader
  pcm.go              FLAC / WAV / MP3 decoders → float32
  resample.go         Parallel polyphase Kaiser-sinc PCM resampler
  ring.go             Lock-free SPSC ring buffer
  safety.go           DC blocker + soft limiter
  metadata.go         Tag + album-art extraction (dhowden/tag, with DSF offset path).
                      Public ReadMetadata + ReadFileMetadata (DSD-aware via ParseDSD)
  lyrics.go           .lrc / embedded / LRCLIB chain (duration-aware /api/search
                      fallback, [offset:±N] honored, v5 cache namespace embedded
                      in the SHA-1 hash so version bumps auto-invalidate without
                      leaving orphan subdirs, ClearLyricsCacheFor for the UI's
                      manual "Retry lookup" path, track-number prefix stripping
                      for "09 - Song.flac" style names)
  visualizer.go       Sample ring + Waveform() peak envelope + radix-2 FFT (latter
                      unused by UI but retained for reuse)

library/
  library.go          Music collection index — Track / Album / Artist structs,
                      Scan(root, progress) two-pass walk (count then tag-read),
                      folder-cover fallback (cover.jpg / folder.jpg / album.jpg /
                      front.jpg + .jpeg/.png variants), gob Save/Load to
                      %APPDATA%\gomusic\library.gob, versioned wire format

playlist/
  playlist.go         Track list (recursive AddDir, file extension allowlist,
                      duplicate-path dedup), Shuffle toggle with Fisher–Yates
                      permutation pinned at current track, SetCurrent for manual
                      jumps, PeekNext for crossfade preload, Move(from, to) for
                      keyboard reordering

ui/
  theme.go            Custom dark theme (Spotify-ish green accent, 28 px heading,
                      8 px padding — fallback theme when no album art is loaded)
  dominant.go         Album-art dominant-color extractor: decodes the cover bytes,
                      samples on a 64×64 grid, discards near-black/white/gray
                      pixels, weights remaining samples by saturation, and returns
                      the centroid of the heaviest histogram bucket. Plus tint()
                      and brighten() helpers for the gradient / accent overlays.
                      Drives the album-aware theming across the player.
  window.go           Fyne layout, three-tab left sidebar (Playlist / Albums /
                      Artists via container.AppTabs), transport (prev/play/next/
                      stop + shuffle + repeat with dot indicators), seek-able
                      progress slider with EXCLUSIVE/SHARED mode indicator,
                      click-to-seek waveform whose glow tints with the album,
                      keyboard shortcuts (Space, arrows, S/R/L, Ctrl+F, Del,
                      Ctrl+Shift+arrows, Ctrl+arrows), playlist search bar (live
                      filter with index remap), depth-of-field animated lyrics
                      panel with Retry-lookup button, syncListSelection that
                      keeps the visible playlist row aligned with pl.Current
                      across auto-advance / next / prev / crossfade, custom
                      squareCenterLayout for responsive album-art sizing,
                      vertical canvas.LinearGradient backdrop in the right
                      column driven by the dominant color, menu (File: Open /
                      Set Music Root / Rescan / Clear; Edit: Search / Move /
                      Remove / Crossfade toggle / Exclusive toggle), waveform
                      animation loop, drag-and-drop, inline SVG icons (white
                      fill so Fyne can't tint them with the accent color)
  library.go          Albums tab (widget.GridWrap of cover thumbnails with
                      fixed-size cells via container.NewGridWrap, in-memory
                      coverResource cache to avoid re-decoding on scroll),
                      Artists tab (widget.List of artist names + album count),
                      drill-down (click artist → filter Albums tab + jump tab),
                      confirmLoadAlbum (replace playlist + play), background
                      scanLibrary with progress dialog
  config.go           %APPDATA%\gomusic\config.json — volume, playlist paths +
                      current index, lyrics-panel visibility, crossfade duration,
                      exclusive-mode preference, music root. Defaults pre-set
                      before unmarshal so older configs upgrade cleanly
  waveform.go         Mirrored polyline visualizer with glow + edge fade + tap-
                      to-seek; allocation-free per-frame updates
  mediakeys_windows.go System media keys (Play/Pause/Next/Prev/Stop) registered
                      via Win32 RegisterHotKey on a runtime.LockOSThread-pinned
                      goroutine pumping GetMessageW. Works globally regardless
                      of focus; also catches most Bluetooth headphone transport
                      buttons.
  mediakeys_other.go  Stub for non-Windows builds.
  go_music_icon_1.ico The .exe icon (consumed by rsrc → .syso)
  go_music_icon_1.png The in-app window icon (Fyne can't decode .ico)

resource_windows_amd64.syso   Generated by `rsrc` — embeds .ico into the .exe
main.go                       Entry point — calls ui.Run()

webui/                        Wails 2 + Svelte 5 UI (branch webui-migration).
                              Separate Go module; imports audio/, playlist/,
                              library/ via `replace gomusic => ../`. Zero changes
                              to the audio engine packages.
  app.go                      App struct — all bound methods (transport, playlist
                              ops, settings, retry-lyrics, save/load config).
                              Engine callbacks wired to runtime.EventsEmit.
                              GetSpectrum + GetWaveform are pull-based: frontend
                              polls them inside requestAnimationFrame (no ticker
                              goroutine — that caused jitter under IPC load).
                              Window title set dynamically to "Artist — Title".
  config.go                   %APPDATA%\gomusic\config-web.json — volume, playlist
                              paths + current, exclusive pref, crossfade, shuffle,
                              repeat, musicRoot. Loaded in startup() before
                              callbacks wire; saved in shutdown() and on the
                              frontend's pagehide event.
  library.go                  ScanLibrary, GetLibraryAlbums, GetLibraryArtists,
                              GetLibrarySongs (flat list), LoadAlbum (replace
                              playlist + play). Reads the same library.gob the
                              Fyne UI writes, so a single scan serves both.
  dominant.go                 Dominant-color extractor ported from ui/dominant.go,
                              no Fyne dependency, returns CSS hex.
  main.go                     Wails entry point: 1080×720, min 720×500;
                              DragAndDrop{EnableFileDrop:true}; default WASAPI
                              shared mode (exclusive is an explicit user choice).
  wails.json                  name + outputfilename + info.productName 'Go Music'.
  build/appicon.png           Window icon (copied from ui/go_music_icon_1.png).
  build/windows/icon.ico      .exe icon (copied from ui/go_music_icon_1.ico).
  frontend/src/App.svelte     Svelte 5 (runes). Layout: clamp() responsive grid
                              — sidebar (Playlist|Songs|Albums|Artists) | player
                              | optional lyrics panel. Player: art with ambient
                              glow + reactive box-shadow, FFT spectrum visualizer
                              with spring physics, smooth waveform overlay,
                              draggable progress bar (mousedown + window listeners),
                              transport with SVG icons, accent-color CSS variables
                              driven by track's dominant color (sidebar/player-bg/
                              lyrics-panel all tint subtly), depth-of-field lyrics
                              (inline-style scale/opacity/blur per line distance),
                              retry-lyrics button on LRCLIB miss, keyboard
                              shortcuts (Space/←→/Ctrl+←→/↑↓/S/R/L/Esc).
  build/bin/gomusic-web.exe   Built output (~12 MB).
```

## Roadmap

### Audio engine
- [x] WASAPI exclusive mode for bit-perfect output (via malgo / miniaudio)
- [x] On-screen indicator of exclusive vs. shared mode
- [x] Persisted playlist across sessions
- [x] Seek support for DSD (block-aligned producer restart)
- [x] PCM crossfade
- [x] Library view (album / artist browsing)
- [x] System media keys + keyboard shortcuts
- [x] Album-derived dynamic theming (accent color, ambient backdrop, depth-of-field lyrics)
- [x] Auto-tracking playlist selection (visible row follows the player across every transition)
- [x] Manual lyrics retry button (recover from offline / LRCLIB miss without re-loading)
- [ ] Native DSD output via DoP for compatible DACs
- [ ] Streaming PCM resample (don't block load on long FLACs)
- [ ] Per-band parametric EQ
- [ ] Gapless playback (no fade, just seamless concat for tracks tagged as parts of one album)
- [ ] ReplayGain / loudness normalization
- [ ] DSD crossfade (mixing against the producer ring is harder than the PCM case)

### UI migration — Wails + Svelte 5 (`webui-migration` branch)
- [x] Fase 1: Wails scaffold, Engine bindings, Hello World binary (gomusic-web.exe)
- [x] Fase 2: Full transport, playlist, auto-advance, crossfade preload, album art, lyrics, Canvas2D waveform
- [x] Fase 3: Dynamic accent color, layout responsive, depth-of-field lyrics, ambient glow
- [x] Fase 3b: Albums grid, Artists list with filter, Songs tab with search, exclusive toggle
- [x] Fase 3c: FFT spectrum visualizer with spring physics, smooth waveform overlay
- [x] Fase 4: Config persistence (config-web.json), keyboard shortcuts, draggable progress bar, dynamic window title, retry-lyrics button (per-track cache invalidation)
- [x] Fase 4b: Native app icon embedded (ico + window png), pointer-events bug fixes
- [ ] Fase 5: Media keys (wire existing `ui/mediakeys_windows.go` into Wails startup)
- [ ] Fase 5b: Drag-and-drop reorder, playlist search, crossfade duration slider
- [ ] Fase 6: Cutover — delete `ui/`, remove Fyne from go.mod, merge to main

## Known limitations

- **PCM load time**: 3–6 seconds for a 4-minute FLAC depending on rate, because the entire file is decoded and resampled at load. The UI shows _Loading…_ during this. Streaming PCM is planned.
- **PCM seek has up to ~200 ms latency before the jump is audible in shared mode**: oto's internal buffer is still holding the pre-seek samples; we don't flush it because `oto.Player.Reset()` also silently pauses the player and triggered a separate race with `watchPlayerDone`. The seek itself is instant; only the audible jump is delayed. In exclusive mode the buffer is ~5 ms so this is barely measurable.
- **DSD seek causes a brief audio gap (~0.5–1 s)** while the producer restarts at the new block offset. There is no way around this without preserving the FIR delay-line state across seek positions, which is not worth the complexity.
- **Crossfade is PCM-only.** Tracks where either side is DSD hard-cut between songs.
- **Library scan reads tags for every file in the music root.** ~1000 FLACs scans in 1–3 s on SSD; DSD files take longer because each one parses its header via `ParseDSD` to find the ID3v2 offset. After the first scan the gob cache makes subsequent launches instant.
- **The library does not auto-detect new files in the music root** — `File → Rescan Library` triggers a fresh scan.
- **Bluetooth headphone Play/Pause** works on most headsets via the Win32 hotkey mechanism, but Windows occasionally routes the keypress to the system SMTC (System Media Transport Controls) before our hotkey sees it. When that happens the key reaches whichever app currently "owns" SMTC instead of us. There's no clean fix without implementing the SMTC contract ourselves.

## License

Personal project — no license declared yet.
