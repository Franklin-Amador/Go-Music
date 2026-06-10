// ── Action handlers that call the Go backend ─────────────────────────────────
// All functions mutate the shared `s` state object; reactivity flows from there.
import * as Backend from '../../wailsjs/go/main/App.js'
import { s, d } from './stores.svelte'

// ── open / scan ──────────────────────────────────────────────────────────────
export async function openFile() {
  const path = await Backend.OpenFileDialog()
  if (!path) return
  s.errorMsg=''; s.loading=true
  try { s.track = await Backend.LoadFile(path) }
  catch(e:any) { s.errorMsg=String(e); s.loading=false }
}

export async function openFolder() {
  const dir = await Backend.OpenFolderDialog()
  if (!dir) return
  const pl = await Backend.AddFolder(dir)
  s.playlist = pl
  s.tab = 'playlist'
}

export async function scanLibraryDialog() {
  const dir = await Backend.OpenFolderDialog()
  if (!dir) return
  s.scanning=true; s.scanProgress={done:0,total:0}
  Backend.ScanLibrary(dir)
}

// ── transport ────────────────────────────────────────────────────────────────
export async function play()  { try { await Backend.Play() } catch(e:any) { s.errorMsg=String(e) } }
export function pause() { Backend.Pause() }
export function stop()  { Backend.Stop() }

// Single handler — re-evaluates state at click time, not at render time.
export function togglePlay() {
  if (d.isPlaying) pause()
  else play()
}

export async function next() {
  s.loading=true
  try { const t=await Backend.Next(); if(t) s.track=t }
  catch(e:any) { s.errorMsg=String(e) }
  finally { s.loading=false }
}

export async function prev() {
  s.loading=true
  try { const t=await Backend.Prev(); if(t) s.track=t }
  catch(e:any) { s.errorMsg=String(e) }
  finally { s.loading=false }
}

export async function playAt(index:number) {
  s.loading=true
  try { const t=await Backend.PlayAt(index); if(t) s.track=t }
  catch(e:any) { s.errorMsg=String(e) }
  finally { s.loading=false }
}

// ── queue drag-and-drop reorder ──────────────────────────────────────────────
// Reorder is only allowed on the unfiltered list (so displayed indices map
// 1:1 to real playlist indices). HTML5 DnD; the drop commits via MoveTrack.
export function onQueueDragStart(e: DragEvent, index: number) {
  if (s.queueSearch) { e.preventDefault(); return }
  s.dragIndex = index
  if (e.dataTransfer) { e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', String(index)) }
}
export function onQueueDragOver(e: DragEvent, index: number) {
  if (s.dragIndex === null) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  s.dragOverIndex = index
}
export function onQueueDrop(e: DragEvent, index: number) {
  e.preventDefault()
  if (s.dragIndex !== null && s.dragIndex !== index) {
    Backend.MoveTrack(s.dragIndex, index).then(pl => { s.playlist = pl })
  }
  s.dragIndex = null; s.dragOverIndex = null
}
export function onQueueDragEnd() { s.dragIndex = null; s.dragOverIndex = null }

// ── named playlists ───────────────────────────────────────────────────────────
export function refreshLists() { Backend.GetPlaylists().then(p => { s.playlists = p ?? [] }) }

export async function openList(name: string) {
  if (s.openListName === name) { s.openListName = null; return }
  s.openListName = name
  s.openListTracks = await Backend.GetPlaylistTracks(name) ?? []
}
export async function playList(name: string) {
  s.tab = 'playlist'
  try { const t = await Backend.PlayPlaylist(name); if (t) s.track = t }
  catch(e:any) { s.errorMsg = String(e) }
}
export function queueList(name: string) { Backend.QueuePlaylist(name) }
export function deleteList(name: string) {
  Backend.DeletePlaylist(name)
  if (s.openListName === name) { s.openListName = null; s.openListTracks = [] }
}
export async function createList() {
  const name = s.newListName.trim()
  if (!name) return
  const ok = await Backend.CreatePlaylist(name)
  if (ok) { s.newListName = ''; s.creatingList = false }
  else    { s.errorMsg = `A playlist named “${name}” already exists` }
}
export function removeFromList(name: string, path: string) {
  Backend.RemoveFromPlaylist(name, path)
  s.openListTracks = s.openListTracks.filter(t => t.path !== path)
}

// add-to-playlist menu
export function openAddMenu(path: string, title: string) {
  s.addMenuPath = path; s.addMenuTitle = title; s.addMenuNew = ''
}
export function addToList(name: string) {
  if (!s.addMenuPath) return
  Backend.AddToPlaylist(name, [s.addMenuPath])
  s.addMenuPath = null
}
export async function addToNewList() {
  const name = s.addMenuNew.trim()
  if (!name || !s.addMenuPath) return
  await Backend.AddToPlaylist(name, [s.addMenuPath])  // creates if missing
  s.addMenuPath = null
}

// ── library ──────────────────────────────────────────────────────────────────
export async function loadAlbum(artist:string, title:string) {
  s.loading=true; s.tab='playlist'
  try { const t=await Backend.LoadAlbum(artist, title); if(t) s.track=t }
  catch(e:any) { s.errorMsg=String(e) }
  finally { s.loading=false }
}

// Play an ordered list (Songs view, expanded playlist) starting at `index`.
// The list becomes the queue so Next/Prev walk it.
export async function playSongs(paths:string[], index:number) {
  s.loading=true; s.tab='playlist'
  try { const t=await Backend.PlaySongs(paths, index); if(t) s.track=t }
  catch(e:any) { s.errorMsg=String(e) }
  finally { s.loading=false }
}

export function filterByArtist(artist:string) { s.artistFilter=artist; s.tab='albums' }
export function clearArtistFilter() { s.artistFilter='' }

// ── seek (commit-on-release) ─────────────────────────────────────────────────
// Progress bar — mousedown + window listeners (NOT setPointerCapture, which
// in WebView2 swallows subsequent click events and leaves the player feeling
// "stuck" until the user clicks elsewhere).
//
// The seek is COMMITTED ON RELEASE, not on every mousemove. During the drag
// we only move the optimistic `seekPreview` (so the bar tracks the cursor
// with zero latency). Committing once matters most for DSD: each Seek there
// tears down and restarts the streaming producer, so seeking on every
// mousemove would stutter badly. After release we hold the preview until the
// engine's position converges (see handlePositionChange below).
let seekTarget   = 0   // last committed seek target (s); plain, non-reactive
let seekHoldUntil = 0  // ms deadline after which we stop holding the preview

function previewFromMouse(clientX: number, el: HTMLElement): number {
  const r = el.getBoundingClientRect()
  const frac = Math.max(0, Math.min(1, (clientX - r.left) / r.width))
  return frac * s.dur
}

export function onProgressMouseDown(e: MouseEvent) {
  if (!s.dur) return
  const trackEl = e.currentTarget as HTMLElement
  s.dragging = true
  s.seekPreview = previewFromMouse(e.clientX, trackEl)

  const onMove = (ev: MouseEvent) => { s.seekPreview = previewFromMouse(ev.clientX, trackEl) }
  const onUp   = (ev: MouseEvent) => {
    const target = previewFromMouse(ev.clientX, trackEl)
    s.dragging = false
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup',   onUp)
    seekTarget   = target
    seekHoldUntil = Date.now() + 1200
    s.seekPreview  = target          // hold the preview until the engine catches up
    Backend.Seek(target)
  }
  window.addEventListener('mousemove', onMove)
  window.addEventListener('mouseup',   onUp)
}

// Wired to the `position-change` Wails event by App.svelte.
export function handlePositionChange(p:{pos:number;dur:number}) {
  s.pos = p.pos; s.dur = p.dur
  // Release the optimistic preview once the engine's reported position
  // converges on the seek target (or a safety timeout elapses). Never
  // while still dragging — the user is in control of the value then.
  if (s.seekPreview !== null && !s.dragging &&
      (Math.abs(s.pos - seekTarget) < 0.75 || Date.now() > seekHoldUntil)) {
    s.seekPreview = null
  }
}

// ── misc controls ────────────────────────────────────────────────────────────
export function onVolumeInput(e:Event) {
  s.volume = parseFloat((e.target as HTMLInputElement).value)
  Backend.SetVolume(s.volume)
}

export function retryLyrics() {
  if (!s.track) return
  s.lyricsRetrying = true
  Backend.RetryLyrics()
}

export function toggleShuffle()  { s.isShuffle=!s.isShuffle; Backend.SetShuffle(s.isShuffle) }
export function toggleRepeat()   { s.isRepeat=!s.isRepeat;   Backend.SetRepeat(s.isRepeat) }
export function toggleExclusive(){ s.isExclPref=!s.isExclPref; Backend.SetExclusive(s.isExclPref) }

export function onCrossfadeInput(e:Event) {
  s.crossfade = parseFloat((e.target as HTMLInputElement).value)
  Backend.SetCrossfade(s.crossfade)
}
export function setVisualizerMode(m:'bars'|'wave'|'radial') { s.visualizerMode=m; Backend.SetVisualizerMode(m) }
export function setAccentSource(src:'auto'|'fixed')         { s.accentSource=src; Backend.SetAccentSource(src) }

export function loadDevices() {
  s.devicesLoading = true
  Backend.ListOutputDevices()
    .then(dv => { s.outputDevices = dv ?? []; s.devicesLoading = false })
    .catch(() => { s.devicesLoading = false })
}
export function onSelectDevice(e:Event) {
  s.selectedDevice = (e.target as HTMLSelectElement).value
  Backend.SetOutputDevice(s.selectedDevice)
}

export function fmtTime(sec:number) {
  if (!sec||sec<0) return '0:00'
  return `${Math.floor(sec/60)}:${Math.floor(sec%60).toString().padStart(2,'0')}`
}
