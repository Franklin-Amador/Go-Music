# AGENTS.md

Notes for AI coding agents working on Go Music. Read this once before making changes; it's the kind of context that takes ages to rebuild from scratch.

## What this project is

A high-resolution music player. The reason it exists is that DSD/DSF playback in the previous Python implementation was unstable — GIL pauses and GC interfered with the audio callback timing, producing dropouts and harsh artifacts. The user cares about audio quality first; everything else is secondary.

**When you're tempted to add a feature or refactor for cleanliness, ask: "does this make DSD playback worse?"** If you're not sure, find out before merging.

## Hard constraints

These are non-negotiable. Violating them will break audio quality, playback safety, or playback itself.

1. **The audio callback does no DSP.** It is a `memcpy` (oto) or a fixed-rate per-sample loop in `pcmReader.Read` / `dsdStreamReader.Read` (malgo callback). All decoding, decimation, FIR filtering, channel adaptation, resampling, DC blocking, limiting, and visualizer pushing happens in the producer goroutine. The reader applies volume and (in crossfade) a per-frame gain ramp + one summation — those are the only per-sample ops on the reader/audio-thread side. Adding more work will cause underflows and audible glitches, especially in exclusive mode where the buffer is only ~5 ms.

2. **DSD goes through ONE filter stage, not two.** The pipeline is `bits → long Kaiser FIR (decimating, runs at fs_DSD) → output`. There is NO block-mean / pre-decimation step. There is NO second post-decimation FIR. Adding either reintroduces audible noise aliasing — block-mean has only ~−18 dB stopband in the worst alias bands, and any second resampling stage compounds the leakage. The single long FIR (`DSDDecimatingFIR` in `audio/dsd_decimator.go`) is the entire DSD-to-PCM path. Output rate is chosen so every DSD rate decimates by an integer factor.

3. **The output rate is fixed at 176 400 Hz** (`outputSampleRate` in `audio/engine.go`). It's not arbitrary — it's the lowest rate that satisfies all of:

   - Integer divisor of every DSD rate (DSD64 ÷ 16, DSD128 ÷ 32, …)
   - Clean integer multiple of 44 100 (so 44.1 kHz PCM upsamples cleanly ×4)
   - Clean integer multiple of 88 200
   - Supported by Windows WASAPI exclusive AND shared on the user's DAC

   If you change it, double-check all four properties hold.

4. **`oto.NewContext` can only be called once per process.** That's why `ensureOtoCtxOnce` exists. Don't try to recreate the context for a different sample rate when a new track loads — that path was tried and silently fails. All sources must adapt to the fixed output format.

5. **The Kaiser FIR `cutoff` parameter is normalized to Nyquist (fs/2)**, scipy-style. So a value of `2 * 25_000 / fs_DSD` means an actual cutoff of `25 000 Hz`. There was an off-by-2 bug in this convention earlier — don't reintroduce it. The formula in `buildKaiserFIR` is `sin(π·cutoff·t)/(π·t)`, NOT `sin(2π·cutoff·t)/(π·t)`.

6. **The DSD reconstruction cutoff is 25 kHz, not 30 kHz.** This is intentional. DSD's noise-shaping spectrum starts rising sharply around 20 kHz — the 25 kHz cutoff suppresses it without sacrificing audible content (humans top out at ~20 kHz). The constant `dsdReconstructionCutoffHz` in `audio/dsd_decimator.go` controls this. Raising it to 30+ kHz lets shaped noise through at the high end of the audible band.

7. **The safety chain (`audio/safety.go`) is mandatory.** Every PCM sample that reaches the ring buffer has passed through DC blocker → soft limiter, in that order. Removing or bypassing them risks DC injection (kills tweeters) or hard clipping (square-wave harmonics — harsh distortion plus tweeter damage). The chain is not user-configurable; do not add a toggle for it. Yes, this means we're not 100% bit-perfect in exclusive mode — see the README. The limiter only acts on `|sample| > 0.95` so most music is unaffected, and the DC blocker is a single-pole HPF at ~5 Hz with no audible effect.

8. **`OnFinished` must fire exactly once per playback.** Tracks auto-advance, so a double-fire skips a track. The `finishedSignaled atomic.Bool` flag on the Engine guards this. It's reset in `Play()` and CompareAndSwap'd to true the first time the player drains. Don't add another OnFinished call site that bypasses the flag. **For crossfade transitions OnFinished does NOT fire** — `OnTrackTransition` does, in the audio thread, the moment the new track first becomes audible. OnFinished is only for the natural end of the last track (no preload available).

9. **All UI mutations from non-UI goroutines must go through the correct dispatcher for the active frontend.**

   - **Fyne UI (`ui/`):** `fyne.Do(func() { ... })`. Engine callbacks, the waveform ticker, lyric animation callbacks, async lyrics fetches, media-key callbacks, and the malgo audio thread all run off the UI thread. Calling Fyne widget methods directly from those will corrupt the render tree. Fyne's `Animation.Start` callbacks dispatch onto the UI thread automatically — those don't need an extra `fyne.Do`.

   - **Wails UI (`webui/`):** `runtime.EventsEmit(ctx, eventName, data)`. The Wails runtime posts the event into the WebView's JS message queue on the correct thread. Never call `runtime.EventsEmit` from within a goroutine that doesn't have `ctx` from `App.startup` — store it in `App.ctx` and always use that field. Never call Wails runtime functions from inside engine callback closures *synchronously*; always call through the stored ctx.

10. **The visualizer's `Push` is the only piece of audio-thread code that's allowed to call into anything outside the producer.** It is pure memcpy into a lock-free ring (no allocation, no FFT, no peak detection). `Snapshot()` (FFT) and `Waveform()` (peak envelope) both run from the UI ticker — never from a Reader or a producer.

11. **`Engine.Position()` subtracts the player's `BufferedSize()`** so the reported position matches what the DAC is emitting, not what the reader has handed downstream. Without it, synced lyrics flip early (especially in oto shared mode where the buffer is ~200 ms). The `audioPlayer` interface enforces this — both `*oto.Player` and `*malgoPlayer` implement `BufferedSize() int`. In exclusive mode the compensation is ~5 ms (negligible) but the code path is identical.

12. **`oto.Player.Reset()` clears the buffer AND silently pauses the player.** That second behaviour is not documented but is in the v3 source. Don't call Reset during a PCM seek to flush the stale buffer — every attempt at "Reset then Play" raced with `watchPlayerDone` reading `IsPlaying()==false` and firing `OnFinished`, auto-advancing on every seek. The accepted trade-off is leaving the buffer alone so the seek has up to ~200 ms audible delay before the jump in shared mode. (Exclusive mode's buffer is ~5 ms so it's not visible.)

13. **DSD seek requires the old producer to be fully gone before a new one starts.** The `dsdDone` channel on Engine is closed by the producer's defer; `stopInternal` waits on it (with a 500 ms timeout) so the producer's tail-end writes to `e.dsdEOF`/`e.dsdReady`/`e.ring` can't clobber a freshly-restarted playback's state. Removing the wait reintroduces a phantom-EOF race that manifests as "tracks skip to the next one when seeked".

14. **The `audioPlayer` interface is the ONLY way the engine talks to the backend.** Code in `engine.go` must never type-switch on `*oto.Player` vs `*malgoPlayer`. The engine doesn't know which one is active and shouldn't care. If a feature needs backend-specific behaviour, add a method to the interface and implement it in both. `exclusive.go` is the only file that depends on malgo; `engine.go`'s oto dependency is similarly contained.

15. **Backend selection happens in `Engine.openPlayer(io.Reader)`, NOT in the constructor.** `NewEngine` doesn't open any device. The first `Play()` call invokes `openPlayer`, which (a) honors `exclusiveDisabled atomic.Bool` — if true, skips malgo entirely and uses oto; (b) otherwise tries `newExclusivePlayer` first and falls back to oto on failure. Don't move device opening to startup — it would lock the DAC for the whole session even when paused, and it would mean failing-to-launch when the user already has another exclusive-mode app open.

16. **Crossfade lives ONLY in `pcmReader.Read`.** Not in dsdStreamReader (DSD always hard-cuts), not in a separate mixer goroutine, not in the audio backend. The mix is done per-frame inside Read so it works identically with malgo or oto without either of them knowing crossfade exists. `Engine.Preload` and `Engine.tryStartCrossfade` mutate the engine's `next*` / `outgoing*` slots under `cfMu`; the reader takes `cfMu` briefly only at fade-start and fade-end, never during steady-state mixing. The fade-disabled fast path takes zero locks and is byte-identical to pre-crossfade behaviour.

17. **`Engine.Info` is written from the audio thread during crossfade** (in `tryStartCrossfade`). Pointer assignment on amd64 is atomic, and the audio thread's `pcmPos.Store` immediately afterwards provides the necessary memory ordering for the UI side to observe the new pointer. The race detector flags it but the actual behaviour is correct. Don't try to "fix" it with a mutex — the audio thread can't tolerate lock contention on every Position() call from the UI ticker.

18. **`OnTrackTransition` advances the playlist on the UI side, NOT in the engine.** The engine fires the callback with the new `FileInfo`; the UI handler matches the path against `pl.Tracks` and calls `pl.SetCurrent` itself. This keeps the engine ignorant of the playlist abstraction (which is right — same engine should work with a future "queue" or "radio" feature without modification). Don't move `pl.SetCurrent` into the engine.

## Build

### Fyne UI (`gomusic.exe`)

```powershell
go build -ldflags="-H windowsgui -s -w" -o gomusic.exe .
```

`-H windowsgui` flags the binary as a Windows GUI subsystem app so launching
it does NOT spawn an attached console window. `log.Printf` output is silently
discarded in this mode — if you need logs while debugging, build without
`-H windowsgui` (or redirect log output to a file).

### Wails UI (`gomusic-web.exe`) — branch `webui-migration`

Requires Wails CLI v2 and WebView2 Runtime (ships with Windows 11; download the evergreen bootstrapper for Win10).

```powershell
# Install Wails CLI once
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Build from the webui/ subdirectory
cd webui
wails build
# → build/bin/gomusic-web.exe (~12 MB debug, use -ldflags="-s -w" for production)
```

For dev mode with hot-reload frontend (Go recompiles on save):
```powershell
cd webui
wails dev
```

The Wails build uses the same MinGW GCC that the Fyne build needs — no extra
toolchain required. The WebView2 runtime must be installed on the target
machine (it ships with Windows 11 automatically; on Windows 10 install the
[WebView2 Runtime](https://go.microsoft.com/fwlink/p/?LinkId=2124703)).

Requires GCC for both the Fyne UI AND the miniaudio C source that `gen2brain/malgo` wraps. **GCC 16.x from w64devkit is incompatible with Go's cgo** — it produces COFF objects Go can't parse. Use MinGW from Chocolatey (GCC 14.x) instead:

```powershell
choco install mingw -y     # Administrator shell
```

After installing, the new GCC takes priority — verify with `gcc --version`.

### Distributing to another Windows PC (UCRT gotcha)

The MinGW-w64 15.x toolchain links against the **Universal C Runtime** — the .exe imports `api-ms-win-crt-*.dll`. Those ship with Win11 and Win10 ≥ 1809 but are missing on older Win10 installs without recent Windows Updates. Symptom: the .exe silently fails to launch on the target PC.

Fix on the target machine: install **VC++ Redistributable 2015-2022 (x64)** (`https://aka.ms/vs/17/release/vc_redist.x64.exe`) — it bundles UCRT. One-time install, every subsequent rebuild works.

`-extldflags=-static` in `-ldflags` does NOT solve this: MinGW-UCRT has no static UCRT library because UCRT is a Windows system component, not a user-space library. The only build-side fix is switching to a MSVCRT-based MinGW (`pacman -S mingw-w64-x86_64-toolchain` in MSYS2), which produces a .exe that works on every Windows from XP onward at the cost of swapping toolchains. Not the default here.

The Windows .exe icon comes from `resource_windows_amd64.syso`, which `go build` links automatically. To regenerate (after replacing the icon):

```powershell
go install github.com/akavel/rsrc@latest
rsrc -ico ui/go_music_icon_1.ico -arch amd64 -o resource_windows_amd64.syso
```

The in-app window/title-bar icon is a separate **PNG** (`ui/go_music_icon_1.png`) `//go:embed`-ed in `ui/window.go`. Fyne's icon renderer goes through Go's `image.Decode`, which has no `.ico` decoder in stdlib — handing it `.ico` bytes paints a black rectangle on the title bar. Regenerate from the `.ico` whenever the icon changes:

```powershell
Add-Type -AssemblyName System.Drawing
$ico = New-Object System.Drawing.Icon("ui\go_music_icon_1.ico", 256, 256)
$ico.ToBitmap().Save("ui\go_music_icon_1.png", [System.Drawing.Imaging.ImageFormat]::Png)
```

## How to test changes

There is no automated test suite yet. Manual smoke tests on the user's library:

- `C:\Users\joela\Music\A1.Take On Me.dsf` — DSD128 stereo, 3:48, with embedded JPEG art (a-ha)
- `C:\Users\joela\Music\01. Hotel California.dsf` — DSD64 stereo, no embedded art (Eagles)
- `C:\Users\joela\Music\09 - T.N.T..flac` — 96 kHz / 24-bit FLAC, with embedded art (AC/DC)
- `C:\Users\joela\Music\05 - I'LL BE GONE.flac` — 44.1 kHz / 24-bit FLAC
- `C:\Users\joela\Music\Deezer\Queen - Killer Queen.mp3` — MP3 44.1 kHz, large embedded art (used to verify recursive `AddDir`)

A quick load benchmark can be made by writing a small `cmd/bench` driver that calls `audio.Engine.Load()` with timing — useful for catching resampler regressions. Don't commit it.

When you change anything in `audio/`, listen for:

- A click/burst at the start of DSD playback → warm-start regression. The decimator must initialize the FIR delay-line state to zeros (smooth fade-in from silence), not to the first DSD bit value (DC step).
- Constant buzz throughout DSD playback → block-mean / boxcar decimation was reintroduced, or a second resampling stage exists after the long FIR.
- Distortion on PCM → resampler kernel is too small or cutoff scaling is off, or hard clipping (safety chain bypassed).
- Glitches / dropouts → DSP slipped into the audio callback, or the producer is starving (too much work between chunks). In exclusive mode this is much more sensitive than shared mode because the buffer is only ~5 ms.
- Track skipping in auto-advance → `OnFinished` is firing twice; the `finishedSignaled` flag isn't being checked.
- UI tearing / corruption → a goroutine called into Fyne widgets without `fyne.Do`.
- Synced lyrics flip before/after the line is sung → the `BufferedSize()` compensation in `Engine.Position()` was lost, or an LRCLIB record from a different release was accepted (check duration delta vs the track).
- Waveform line frozen during playback → `Visualizer.Push` no longer reached from the producer, or the `runWaveformLoop` goroutine isn't started in `Run()`.
- **Tracks loop inside the first second / never advance past ~1 s** → the `updatingUI` guard on the progress slider's `OnChangeEnded` was removed. The position timer's programmatic `SetValue` fires OnChangeEnded → eng.Seek → pulls pcmPos backwards by BufferedSize (PCM) or forces a full producer restart (DSD). Restore the guard in `ui/window.go`.
- Clicking the progress bar skips to the next track → the `oto.Player.Reset()` race with `watchPlayerDone` came back. Don't Reset in Seek; tolerate the buffered-audio delay.
- DSD seek causes the next track to load instead of seeking → the `dsdDone` wait in `stopInternal` was removed, so the old producer's tail-end stored `dsdEOF=true` over the new producer's state.
- **Audible gap between tracks when crossfade is on** → `Preload` is not being called early enough, OR `pcmReader.Read` is hitting EOF on the current track before the preload completes its background decode. Check the `maybePreloadNext` threshold (currently `crossfadeSec + 6 s`); raise it if your storage is slow.
- **Pop/click at the start of a crossfaded next track** → the new track's first sample is non-zero (almost always true) and we're suddenly mixing it in at gain ~0.001 instead of true zero. Should be inaudible at the start of the ramp. If it's audible the ramp got too short OR the mix loop is computing gain incorrectly — verify `gainOut = (outRem - i) / fadeTotal` clamps to [0, 1].
- **The previous track keeps playing for 4 seconds after the user clicks "next"** → manual track change isn't calling `Engine.ClearPreload()`. `loadAndPlay` does this; some new code path you added probably doesn't.
- **Other apps go mute when I start playback** → expected behaviour in exclusive mode. Surface the toggle (`Edit → Exclusive mode`) to the user, not a bug.
- **The app fails to launch when another exclusive-mode app is running** → `newExclusivePlayer` should fall back to oto, not propagate the error. Check `openPlayer` still does the try/fallback dance.
- **Albums grid empty or shows only text** → `widget.GridWrap` cell sizing collapsed. Check that `container.NewGridWrap(coverSize, ...)` wraps the cover area inside the cell (not just relying on canvas.Image.SetMinSize), and that the canvas.Text widgets have placeholder strings at creation time.
- **No covers in the album grid even for albums that have them** → check whether the user's library uses folder covers (`cover.jpg` next to the FLAC) instead of embedded art. The `library.fillFolderCovers` second pass handles this; if it was disabled or the filename patterns don't match, those albums fall through to the placeholder glyph.

## Code style

- Comments explain _why_, not _what_. The code already says what it does.
- For audio code specifically, comments should anchor to the signal-processing reasoning ("this Kaiser β gives ~115 dB stopband which is below the noise floor of any real DAC").
- Prefer `float32` throughout the DSP path. Convert to `float64` only when you genuinely need the precision (rare). The conversions cost real cycles.
- Allocations in the audio callback or producer hot loop are not allowed. The visualizer ring uses a fixed buffer; the waveform widget pre-allocates every `canvas.Line` once and mutates `Position1`/`Position2` per frame; the polyphase resampler precomputes kernels.

## Things explicitly NOT to do

- **Do not add a "convenience" linear resampler.** Linear interpolation aliases high-frequency content into the audible band — catastrophic for DSD. The polyphase Kaiser-sinc resampler in `resample.go` is the only resampler.
- **Do not reintroduce block-mean / boxcar decimation for DSD.** It was the root cause of the audible buzz throughout DSD playback. The single long Kaiser FIR in `dsd_decimator.go` replaces it.
- **Do not change the output rate to 48 000 Hz** "to match the OS". That makes DSD decimation non-integer and brings back exactly the buzz that took two iterations to remove.
- **Do not call `oto.NewContext` more than once.** It silently breaks playback on the second call.
- **Do not add per-sample work in the audio callback** (volume, EQ, dithering, anything). Move it to the producer.
- **Do not remove or bypass the DC blocker or soft limiter.** They protect tweeters and ears. Hard clipping is never an acceptable output.
- **Do not push the FFT or waveform peak-detection into the audio thread.** `Snapshot()` and `Waveform()` are called by the UI ticker, period. `Push()` is the only thing the producer touches on the visualizer.
- **Do not run the waveform UI loop above 30 fps** without re-checking lag on the user's machine. Fyne is single-threaded for canvas updates; with ~380 line objects mutated per frame, 60 fps backs up the UI thread on lower-end systems.
- **Do not revive the spectrum-bar widget.** The user explicitly rejected it as visually "bug-like"; the waveform replaced it. The FFT machinery in `audio/visualizer.go` is kept for potential reuse but the UI does NOT call `Snapshot()`.
- **Do not strip the `oto.Player.BufferedSize()` compensation in `Engine.Position()`.** Without it synced lyrics drift ~200 ms ahead of audio on every track.
- **Do not embed `.ico` into Fyne resources.** Use the PNG (`ui/go_music_icon_1.png`) for `app.SetIcon` / `window.SetIcon`. Go stdlib can't decode `.ico` and Fyne paints a black rectangle.
- **Do not drop the LRC `[offset:±N]` parsing or the LRCLIB duration-aware fallback.** Both fix real, reproducible sync bugs (the "Bad" symptom: lyrics ahead by a constant amount because a wrong-release LRC was accepted).
- **Do not call `eng.Seek` from `OnChangeEnded` without first checking `ui.updatingUI`.** See the footgun above — without the guard, the position timer self-triggers a Seek every 250 ms and playback loops in the first second.
- **Do not call `oto.Player.Reset()` in the Seek path.** Reset pauses the player; the gap before re-Play lets `watchPlayerDone` fire `OnFinished`. The 200 ms of stale buffer audio is the lesser evil.
- **Do not give shuffle/repeat buttons `HighImportance`.** Fyne tints `HighImportance` icons with the accent (green) — the user explicitly rejected that look. Buttons stay `LowImportance` with hard-coded white SVG fill; the active state shows as a small white dot under each button.
- **Do not move `Engine.Info` updates out of `tryStartCrossfade` and into the UI's OnTrackTransition handler.** That was tried; it leaves `e.Info` pointing at the OLD track for one position-timer tick (~250 ms) after the audio has already swapped to the new track. The position display and Duration go briefly out of sync. Better to take the small race-detector hit on the pointer assignment, which is atomic on amd64 anyway.
- **Do not advance the playlist (`pl.Next` / `pl.SetCurrent`) from inside `tryStartCrossfade`.** The engine doesn't know about the playlist. `OnTrackTransition` fires with the new `FileInfo` and the UI handler matches the path against `pl.Tracks` to find the new index. Keeping the engine ignorant of playlist semantics is what lets future "queue" / "radio" features reuse it.
- **Do not call `Engine.Load(nextPath)` from the OnTrackTransition handler** as a "safety net" to re-sync state. Load tears down playback (`stopInternal`) and re-decodes — that defeats the entire point of the crossfade. The engine has already promoted internally; the UI just decorates.
- **Do not try to crossfade DSD tracks.** It's been considered. DSD playback streams through a producer goroutine and a ring buffer; mixing against that requires either pulling the ring contents out (read-pos rewind that the lock-free ring doesn't support) or spinning up a second producer + ring + decimator for the next track and mixing their outputs in the consumer. Both are substantial refactors. PCM crossfade works because the entire track is already in memory at known offsets.
- **Do not eagerly init the oto context in `NewEngine`.** Doing so locks `audiodg.exe` to a shared-mode session that competes with the user's later exclusive-mode request. `ensureOtoCtxOnce` is called lazily inside `openPlayer`'s fallback branch — after `newExclusivePlayer` has already failed or been skipped.
- **Do not type-switch on the player type in engine code.** If you need backend-specific behaviour, add a method to the `audioPlayer` interface and implement it in both `exclusive.go` and the oto path. The engine treats all backends identically.
- **Do not use `dhowden/tag` for DSF files with offset 0.** DSF stores its ID3v2 stream past the audio data, at the offset reported by `ParseDSD().MetadataOffset`. Calling `ReadMetadata(path, 0)` for a DSF file tries to parse the DSF magic bytes as ID3 and silently returns nil. Use `audio.ReadFileMetadata(path)` which figures out the right offset per file extension.
- **Do not block the UI thread during a library scan.** `library.Scan` walks the filesystem and reads tags from potentially thousands of files. The UI calls it via a goroutine that dispatches progress updates with `fyne.Do`. A synchronous scan freezes the window for seconds.
- **Do not store decoded album-art bytes per track in the library.** One copy per album (kept on the first track that has art) keeps the in-memory + on-disk index manageable. Storing per-track art would blow the cache file size up by 10–20×.
- **Do not call `widget.List.Refresh()` / `widget.GridWrap.Refresh()` from the library-scan goroutine directly.** Wrap with `fyne.Do`. Same rule as engine callbacks.
- **Do not use `--no-verify` or skip git hooks** unless the user explicitly asks.

## Things to consider before adding features

- **EQ**: implement as a chain of biquad sections in the producer, AFTER the safety chain (so the limiter still catches anything EQ boost would push over). Each section is ~10 mul-adds per sample per channel, easily fits in the producer's budget. Per-band update can rebuild the biquad coefficients under a lock; the producer reads them lock-free between chunks. Apply only on PCM — DSD's single-FIR pipeline is sacred.
- **Streaming PCM**: decode and resample chunk-by-chunk during playback instead of full-file at load time. Architecturally mirrors the DSD producer path. Would eliminate the 3–6 s "Loading…" delay on large FLACs. Memory drops from O(track length) to O(buffer length). Careful: the current PCM crossfade assumes the entire `pcmData` is resident in memory so it can mix arbitrary offsets — streaming would need a separate buffering layer for the next-track preload, OR crossfade would need to be disabled while streaming is active.
- **Gapless playback**: cheaper variant of crossfade where the next track's first frames are appended seamlessly to the end of the current one (no fade, just concat). Same `Preload` plumbing applies, but `tryStartCrossfade` runs with `fadeFrames = 0` and the reader switches sources mid-buffer. Useful for live albums and prog rock where the track boundary is part of the music.
- **ReplayGain**: read `replaygain_track_gain` / `replaygain_album_gain` tags during `Load` / library scan, apply the linear gain factor in the producer (or in `pcmReader.Read` right alongside volume). Bound the result to [-30 dB, +12 dB] to be safe; the soft limiter will catch anything that still goes over.
- **Native DSD output via DoP**: pack DSD bits into PCM-shaped frames (8-bit DSD samples in the bottom 8 bits of 24-bit PCM with marker bytes 0x05/0xFA in the top). Requires the DAC to advertise DoP support. malgo doesn't expose DoP directly — would need a custom format negotiation or fall back to native DSD via WASAPI's DSD format flag (not all driver stacks expose it).
- **Library auto-rescan**: watch the music root via `fsnotify` and re-index on add/remove. Bigger change than it sounds — gob cache invalidation, incremental updates without rebuilding the whole index, throttling rapid changes. For now the user clicks "Rescan Library" after they add new music.
- **Tracklist popup per album**: right-click an album in the grid → shows track list, lets the user queue individual tracks instead of replacing the whole playlist. The data is already in `library.Album.Tracks`; just needs a popup widget.

## Known footguns

- **`malgo.ShareMode` constants are unprefixed.** It's `malgo.Exclusive` / `malgo.Shared`, NOT `malgo.ShareModeExclusive`. The latter was the natural guess and compiles only with a build error you have to extract from `cgo.exe: exit status 2`. Don't autocomplete from training data here.
- **`malgo.DataProc` signature does NOT include a `*malgo.Device` first argument.** It's `func(output, input []byte, frameCount uint32)`. Got bitten by `func(_ *malgo.Device, output, _ []byte, frameCount uint32)` initially.
- **`malgo.InitContext` is expensive (it probes all audio backends).** Use the `ensureMalgoCtx()` singleton in `audio/exclusive.go` — one context per process, reused across every track. Initializing it eagerly at startup grabs nothing; only `InitDevice` actually touches the DAC.
- **WASAPI exclusive mode and oto can NOT both hold the device at once.** That's why `openPlayer` tries malgo FIRST and only initializes the oto context as a fallback (`ensureOtoCtxOnce` is inside the fallback branch). If you accidentally call `ensureOtoCtxOnce` before the malgo attempt, oto will grab the device in shared mode and the subsequent malgo attempt fails with "device in use" — the user thinks they're getting exclusive but they're not.
- **Hidden `canvas.Image` in a `container.Stack` breaks `MinSize` propagation.** When the album-grid cell's image was Hide()'d at creation, the Stack collapsed to zero height and nothing rendered. Fix: keep the image always Visible, set `Resource = nil` when there's no cover. Fyne then draws nothing for the image and the bg + glyph layer underneath shows through naturally.
- **`widget.GridWrap` cells need explicit sizing via `container.NewGridWrap(size, child)`.** Relying on the inner widgets' MinSize to drive cell dimensions is unreliable — `canvas.Text` with an empty string at creation reports MinSize ~0, the cell shrinks, and recycling other cells doesn't grow it back. The album-grid uses an inner `container.NewGridWrap(fyne.NewSize(140, 140))` to force the cover area to a fixed footprint, plus placeholder text ("Album"/"Artist") in the canvas.Text widgets so the outer cell's text rows are non-zero at first measurement.
- **`runtime.LockOSThread` is mandatory for the media-key message pump.** `RegisterHotKey(nil, ...)` registers the hotkey to the CALLING THREAD; `GetMessage(nil, ...)` retrieves messages for the CALLING THREAD. Without `LockOSThread`, the Go scheduler will move the goroutine between OS threads and the hotkey messages get delivered to a thread that has no pump. The keypress just vanishes.
- `widget.List.Refresh()` in Fyne has to be called on the main goroutine via `fyne.Do(…)`. Calling it from the engine's callback goroutine causes UI corruption.
- `os.ReadDir` returns entries in OS order on Windows — that's not always alphabetical. If track order matters for testing, sort explicitly. (`AddDir` uses `filepath.WalkDir` which has the same caveat.)
- The DFF parser handles bit-interleaved data (no block structure). DSF has per-channel blocks. Easy to mix up — the boolean `DSDInfo.IsDFF` decides which unpacker runs.
- `fyne.io/fyne/v2`'s `Slider.OnChanged` fires both for user drags and programmatic `SetValue` calls. The UI uses an `updatingUI bool` flag to distinguish them so the position slider doesn't fight with the position timer.
- **`Slider.SetValue` ALSO fires `OnChangeEnded`.** Read that twice. Look at `widget/slider.go` in Fyne 2.7: `SetValue` calls `positionChanged` (→ OnChanged) AND `fireChangeEnded` (→ OnChangeEnded). So the same `updatingUI` guard you put on OnChanged MUST also be on OnChangeEnded — otherwise every periodic `OnPositionChange → progress.SetValue(pos/dur)` call from the position timer fires OnChangeEnded, which calls `eng.Seek(pos)`. For PCM that pulls `pcmPos` backwards by `BufferedSize/frameSize` (because `Position()` subtracts the buffered frames), oto's buffer then replays the old samples, and the song "loops" inside the first second of playback. For DSD it forces a full producer restart every 250 ms and playback never gets off the ground. This footgun cost an entire debugging session — keep the guard.
- DSF files have an `ID3v2` tag stream at an offset given in the file header (`metadataOffset`). dhowden/tag doesn't natively parse DSF, but it auto-detects ID3v2 if you wrap the file in an `io.SectionReader` starting at that offset — that's how `audio/metadata.go` reads DSF tags.
- LRCLIB returns HTTP 404 when a song isn't in their database. The cache stores empty strings for negative results so we don't keep hammering the API for songs that will never be there.
- LRCLIB sometimes returns the LRC of a *different release* of the same song (single vs album vs remaster) when durations are within its own tolerance — the result loads cleanly but plays out of sync. `audio/lyrics.go` checks the returned record's duration against the file's and falls back to `/api/search` for a closer match when the delta exceeds `lrclibDurationToleranceSec` (2.5 s). The cache key carries a `lyricsCacheVersion` namespace — bump it whenever the matching logic changes so existing wrong-version cached entries get refetched.
- `fyne.Container.NewBorder(top, bottom, left, right, center)` — the **center** child is what expands. The right column uses this to keep controls at the bottom and let the album art fill the rest of the height.
- **`window.Resize()` taller than the screen's working area gets silently clamped by Windows, and if the content `MinSize` is close to that clamp the user can't shrink the window vertically.** The default size is `1080×720` deliberately — it fits a 1366×768 laptop with taskbar. Bumping the title size, padding, or the album-art MinSize all push the content `MinSize` up, eventually trapping users on small screens with an un-resizable window. If you increase any of those (especially `artBg.SetMinSize` / `artImage.SetMinSize`, currently 180×180), test on a small display before shipping.
- **`canvas.Image` with `FillMode = ImageFillContain` inside `container.NewCenter` is constrained to its `MinSize`, NOT to the available space.** That's why the album art uses a custom `squareCenterLayout` (in `ui/window.go`) instead of `NewCenter`: the custom layout sizes children to the largest square that fits (`min(width, height)` of the parent) and centers them, so the cover grows with the window while keeping its aspect ratio. `NewCenter` alone leaves the art frozen at the MinSize regardless of how big the window gets.
- Fyne's `canvas.NewColorRGBAAnimation` / `fyne.NewAnimation` callbacks already run on the UI thread, so don't wrap them in `fyne.Do` — but DO stop any in-flight animation before starting a new one on the same object, otherwise rapid line changes pile up and flicker. See `mainUI.stopLyricAnims`. The lyric depth-of-field cycle animates a small *set* of lines per transition (active + ±1 + previous-active + previous-±1, deduped via a map); stopping in-flight anims and rebuilding the set keeps the transitions visually continuous even when the user seeks through several lines quickly.
- **`widget.List.Refresh()` repaints rows via `UpdateItem` but does NOT update Fyne's row-hover/selection rectangle.** The selection (the gray highlighted-row visual that Fyne paints on top of UpdateItem's output) only moves when the user clicks OR when code calls `widget.List.Select(rowID)`. So after a programmatic `pl.SetCurrent(i)` — auto-advance, prev/next, crossfade — you have to call BOTH `Refresh()` (for the `▸` prefix + accent color) AND `Select(rowID)` (for the system hover). The helper `syncListSelection()` does both, with `realToRow()` translating the canonical playlist index through any active search filter and `UnselectAll()` when the current track is filtered out. **Watch out: `List.Select(id)` synchronously fires `OnSelected`**, which would re-enter `loadAndPlay` and loop forever. The `suppressSelect bool` flag on `mainUI` short-circuits OnSelected during programmatic selection.
### Wails UI footguns (webui/)

- **`EnableFileDrop` is NOT a field of `options.App`.** It lives inside a nested `options.DragAndDrop` struct: `DragAndDrop: &options.DragAndDrop{EnableFileDrop: true}`. Putting it at the top level produces a compile error that looks like a typo rather than a wrong nesting level.
- **Svelte 5 has no event modifiers.** `onclick|stopPropagation` is a parse error. Use `onclick={(e) => { e.stopPropagation(); handler() }}` instead.
- **Import paths from `frontend/src/` to `frontend/wailsjs/` are ONE `../` up, not two.** `frontend/src/App.svelte` → `../wailsjs/...`. Two levels (`../../wailsjs/`) escapes the `frontend/` directory entirely and Rollup silently fails to resolve the module.
- **`wails build` regenerates `wailsjs/go/main/App.js` and `App.d.ts` every time.** Any hand-edits to those files will be overwritten. The models file (`wailsjs/go/models.ts`) is also auto-generated. Write the types you need inline in the `.svelte` file or in a local `types.ts` instead.
- **Wails bound methods that return `error` are exposed as rejected Promises in JS.** Wrap calls in `try/catch` or `.catch()`. A method returning only `error` (no data) maps to `Promise<void>` on the JS side.
- **`runtime.OpenDirectoryDialog` (not `OpenFolderDialog`) is the Wails runtime function name.** The Go side uses `runtime.OpenDirectoryDialog`; the bound App method can be named whatever you like.
- **`wails dev` requires Node.js on PATH.** It starts a Vite dev server, proxies asset requests, and rebuilds Go on file changes. It does NOT embed the frontend — the WebView loads from localhost. Production `wails build` embeds everything.
- **The `wailsjs/go/models.ts` file is generated for every exported struct used as a return type.** If a bound method returns `*TrackInfo`, Wails generates `models.ts` with a `main.TrackInfo` interface. Import it from `../wailsjs/go/models` if you want the generated type, or define it inline to avoid coupling to the generated file.

### Fyne UI footguns (ui/) — pre-existing

- **`canvas.LinearGradient` with `EndColor` alpha 0 inside `container.NewStack`** has been reported to cause subtle mouse-position drift on Windows in Fyne 2.7.x. The right-column ambient backdrop uses this pattern — if a user reports "the mouse desyncs from the UI by a few pixels", first verify by temporarily replacing `container.NewStack(ui.bgGradient, ...)` with just the inner border container; if the issue disappears, refactor the backdrop to a `canvas.Rectangle` with a solid `FillColor` tinted to the dominant hue (loses the gradient feel but is hit-test-stable). Also check Windows DPI scaling — Fyne 2.7.x has separate known issues with custom widgets at non-100 % DPI; `setx FYNE_SCALE 1.0` is the standard escape hatch.

## Files at a glance

```
audio/
  dsd.go              DSF/DFF parsers, DSDDecoder iterator
  dsd_decimator.go    Long Kaiser FIR — single-stage DSD→PCM (channel-parallel)
  engine.go           Engine struct, public API, dual-backend openPlayer (malgo
                      preferred, oto fallback), audioPlayer interface, producer
                      goroutines, Position() with BufferedSize() compensation,
                      crossfade (Preload, tryStartCrossfade, outgoing slot),
                      atomic config knobs (SetExclusiveMode, SetCrossfade),
                      OnTrackTransition callback
  exclusive.go        malgoPlayer — WASAPI exclusive backend (miniaudio cgo).
                      Singleton malgo context, dataCallback drives same readers
                      as oto. Implements audioPlayer interface so engine is
                      backend-agnostic.
  pcm.go              FLAC / WAV / MP3 decoders → float32
  resample.go         Parallel polyphase Kaiser-sinc PCM resampler, channel adapter
  ring.go             Lock-free SPSC ring buffer
  safety.go           DC blocker + soft limiter (mandatory, applied to every sample)
  metadata.go         Tag + album-art extraction. ReadMetadata (exported) for
                      FLAC/MP3/WAV at offset 0. ReadFileMetadata wraps it with
                      DSF auto-detection (calls ParseDSD for the ID3v2 offset).
                      Metadata struct includes Title/Artist/AlbumArtist/Album/
                      Year/TrackNum/Lyrics/Picture
  lyrics.go           .lrc / embedded / LRCLIB chain + disk cache + LRC parser;
                      [offset:±N] honored; LRCLIB matching falls back to /api/search
                      when /api/get returns a wrong-duration release; v5 cache namespace
                      embedded in the SHA-1 hash key (so version bumps invalidate cleanly,
                      no orphan subdirs); ClearLyricsCacheFor(artist, title) exposed for
                      the UI's manual "Retry lookup" button to invalidate one entry;
                      track-number prefix stripping (matches "09 - Song.flac" style)
  visualizer.go       Sample ring + Push() (audio-thread) + Snapshot() FFT (unused by UI)
                      + Waveform() peak envelope (used by waveformWidget)

library/
  library.go          Music collection indexer. Track / Album / Artist structs,
                      Scan(root, progress) two-pass walk, folder-cover fallback
                      (cover.jpg / folder.jpg / album.jpg / front.jpg + variants),
                      gob Save/Load to library.gob with version field.

playlist/
  playlist.go         Recursive AddDir (filepath.WalkDir), file extension allowlist,
                      duplicate-path dedup. Shuffle Fisher–Yates with current at
                      head. Move(from, to) for keyboard reorder. PeekNext for
                      crossfade preload.

ui/
  theme.go            Custom dark theme (Spotify-ish green accent, 28 px heading,
                      8 px padding — drives the look when no album art is present)
  dominant.go         Dominant-color extractor for album art: decodes PNG/JPEG/GIF
                      bytes, samples on a 64×64 grid, discards near-black/white/
                      low-saturation pixels, weighted-histogram of the rest, returns
                      the centroid of the heaviest bucket as the accent. Plus
                      tint(c, alpha) and brighten(c, t) helpers used for the
                      gradient backdrop and the brightened text/waveform accents.
  window.go           Fyne layout, three-tab left sidebar (Playlist / Albums /
                      Artists via container.AppTabs), transport (prev/play/next/
                      stop + shuffle/repeat with white-icon dot indicators), seek-
                      able progress slider with updatingUI guards on BOTH OnChanged
                      and OnChangeEnded (see footguns), EXCLUSIVE/SHARED mode label
                      in the time row (Engine.OutputMode atomic), click-to-seek
                      waveform with album-tinted glow, keyboard shortcuts (Space,
                      arrows, S/R/L, Ctrl+F, Del, Ctrl+Shift+arrows, Ctrl+arrows),
                      playlist search bar with live filter (index remap),
                      syncListSelection() that keeps widget.List's hover-row in
                      sync with pl.Current across auto-advance / next / prev /
                      crossfade (also handles the OnSelected re-entry trap via
                      a suppressSelect flag), realToRow() inverse of rowToReal
                      for filter-aware row lookup, custom squareCenterLayout for
                      responsive square album art, applyAccent() that drives the
                      ambient bg gradient + format label + EXCLUSIVE label +
                      waveform glow from the extracted dominant color,
                      setEmptyLyricsWithRetry showing the "Retry lookup" button
                      after a failed fetch (invalidates cache via
                      audio.ClearLyricsCacheFor and re-queries), depth-of-field
                      lyrics animation (active 22 px / near 16 px / idle 14 px),
                      menu (File: Open/Set Music Root/Rescan/Clear; Edit: Search/
                      Move/Remove/Crossfade toggle/Exclusive toggle), waveform
                      animation loop, drag-and-drop, OnTrackTransition handler
                      that updates UI WITHOUT calling Load
  library.go          Albums tab (widget.GridWrap of fixed-size cover cells with
                      cached fyne.Resource per album), Artists tab (widget.List
                      with click → filter Albums + jump tab), confirmLoadAlbum
                      replaces playlist + plays, scanLibrary background goroutine
                      with progress dialog, loadCachedLibrary on startup
  config.go           %APPDATA%\gomusic\config.json — volume, playlist paths +
                      current index, lyrics-panel visibility, crossfade duration,
                      exclusive-mode preference, music root. Defaults pre-set
                      before unmarshal so older configs upgrade cleanly
  waveform.go         Mirrored polyline visualizer with glow + edge fade + temporal
                      smoothing; tap-to-seek; allocation-free per-frame updates
  mediakeys_windows.go System media keys via Win32 RegisterHotKey on a LockOSThread-
                      pinned goroutine pumping GetMessageW
  mediakeys_other.go  No-op stub for non-Windows builds
  go_music_icon_1.ico Source for the .exe icon (consumed by rsrc → .syso)
  go_music_icon_1.png In-app window icon — Fyne can't decode .ico

resource_windows_amd64.syso  Generated by `rsrc` — embeds the .ico into the .exe
main.go                      Entry point — calls ui.Run()

webui/                       Wails 2 + Svelte 5 UI (branch webui-migration).
                             Separate Go module (go.mod: replace gomusic => ../).
                             audio/, playlist/, library/ are imported read-only;
                             NOT a single line in those packages is modified.
  app.go                     App struct bound to JS. Methods: LoadFile, Play,
                             Pause, Stop, Seek, SetVolume, GetVolume,
                             AddFiles, AddFolder, GetPlaylist, PlayAt, Next,
                             Prev, Remove, ClearPlaylist, SetShuffle, SetRepeat,
                             SetExclusive, IsExclusive, SetCrossfade, GetCrossfade,
                             GetOutputMode, OpenFileDialog, OpenFolderDialog.
                             Engine callbacks wired to runtime.EventsEmit in
                             startup(). Waveform 30fps ticker goroutine.
  library.go                 ScanLibrary (background goroutine + scan-progress
                             events), GetLibraryAlbums, GetLibraryArtists,
                             LoadAlbum (replaces playlist + starts play).
                             Reads/writes same library.gob cache as the Fyne UI.
  dominant.go                Port of ui/dominant.go with no Fyne dependency —
                             returns dominant color as CSS hex string "#rrggbb".
  main.go                    Wails entry point: 1080×720, min 720×500,
                             DragAndDrop{EnableFileDrop:true}.
  wails.json                 name=Go Music, outputfilename=gomusic-web
  frontend/
    src/App.svelte           Svelte 5 (runes). Two-column layout: sidebar 270px
                             LEFT (Playlist|Albums|Artists tabs), player RIGHT.
                             Canvas2D waveform, dynamic CSS accent from album art,
                             depth-of-field lyrics, clickable progress bar,
                             Shuffle/Repeat/Lyrics/Exclusive toggles.
    wailsjs/go/main/         Auto-generated TS bindings — regenerated by wails
                             build every time. Do NOT hand-edit these.
  go.mod                     module webui; go 1.25.2; replace gomusic => ../
  build/bin/gomusic-web.exe  Built output (~12 MB).
```

## When in doubt

Ask the user. They care a lot about audio quality and have a good ear — they'll tell you if a change made things worse. They will not tell you politely if it does.
