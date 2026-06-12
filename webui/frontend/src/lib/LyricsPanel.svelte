<script lang="ts">
  import { s, d } from './stores.svelte'
  import { retryLyrics, nudgeLyricOffset, setLyricOffset } from './player'
  import LyricLines from './LyricLines.svelte'

  const offsetLabel = $derived(
    s.lyricOffset === 0 ? '0' : (s.lyricOffset > 0 ? '+' : '') + s.lyricOffset.toFixed(2).replace(/\.?0+$/, '') + 's'
  )
</script>

<!-- ════ RIGHT LYRICS PANEL ══════════════════════════════════════════ -->
{#if s.showLyrics}
  <aside class="lyrics-panel">
    <div class="lyrics-header">
      <span>Lyrics</span>
      <span class="lyrics-header-right">
        {#if s.lyrics.length > 0 && !d.lyricsSynced}
          <span class="unsynced-badge" title="Plain lyrics — no line timing available">Unsynced</span>
        {/if}
        {#if s.lyrics.length > 0 && d.lyricsSynced}
          <span class="sync-ctl" title="Lyric timing — nudge, or use ◎ and click the line you hear">
            <button class="sync-btn" onclick={() => nudgeLyricOffset(-0.25)} title="Lyrics 0.25s earlier">−</button>
            <button class="sync-val" class:sync-val-on={s.lyricOffset !== 0}
                    onclick={() => setLyricOffset(0)}
                    title={s.lyricOffset !== 0 ? 'Reset timing offset' : 'Timing offset'}>{offsetLabel}</button>
            <button class="sync-btn" onclick={() => nudgeLyricOffset(0.25)} title="Lyrics 0.25s later">+</button>
            <button class="sync-btn sync-tap" class:sync-tap-on={s.lyricSyncMode}
                    onclick={() => s.lyricSyncMode = !s.lyricSyncMode}
                    title="Tap-to-sync: click this, then click the lyric line you are hearing">◎</button>
          </span>
        {/if}
        <button class="lyrics-close" onclick={() => s.showLyrics=false}>✕</button>
      </span>
    </div>

    {#if s.lyrics.length > 0}
      {#if s.lyricSyncMode}
        <div class="sync-hint">Click the line you are hearing right now</div>
      {/if}
      <div class="lyrics-list">
        <LyricLines big={false} />
      </div>
    {:else if !s.track}
      <div class="lyrics-empty">
        <span class="lyrics-glyph">♪</span>
        <p>No track loaded</p>
      </div>
    {:else if !s.lyricsFetched || s.lyricsRetrying}
      <div class="lyrics-empty">
        <div class="loading-dots"><span></span><span></span><span></span></div>
        <p>{s.lyricsRetrying ? 'Retrying…' : 'Searching for lyrics…'}</p>
      </div>
    {:else}
      <div class="lyrics-empty">
        <span class="lyrics-glyph">♪</span>
        <p class="lyrics-empty-title">No lyrics found</p>
        <p class="lyrics-empty-sub">No provider had a match, or you were offline.</p>
        <button class="retry-btn" onclick={retryLyrics}>
          <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14">
            <path d="M17.65 6.35A7.958 7.958 0 0012 4a8 8 0 100 16c3.73 0 6.84-2.55 7.73-6h-2.08A5.99 5.99 0 0112 18c-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"/>
          </svg>
          Retry lookup
        </button>
      </div>
    {/if}
  </aside>
{/if}

<style>
  /* ════════════════ LYRICS PANEL (right column) ═════════════════════ */
  .lyrics-panel {
    background: linear-gradient(
      175deg,
      rgba(var(--accent-rgb), 0.06) 0%,
      var(--sf1) 20%
    );
    border-left: 1px solid rgba(var(--accent-rgb), 0.12);
    display: flex; flex-direction: column;
    overflow: hidden;
    animation: slide-in .22s ease-out;
    transition: background 0.6s ease;
  }
  @keyframes slide-in {
    from { opacity: 0; transform: translateX(20px) }
    to   { opacity: 1; transform: translateX(0) }
  }
  .lyrics-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.7rem 1rem; border-bottom: 1px solid var(--border); flex-shrink: 0;
    font-size: 0.65rem; font-weight: 700; letter-spacing: 0.07em;
    text-transform: uppercase; color: var(--muted)
  }
  .lyrics-header-right { display: flex; align-items: center; gap: 0.6rem }

  /* lyric timing calibration controls */
  .sync-ctl {
    display: flex; align-items: center; gap: 1px;
    background: var(--sf2); border: 1px solid var(--border);
    border-radius: 9px; padding: 1px
  }
  .sync-btn {
    background: none; border: none; color: var(--muted); cursor: pointer;
    font-size: 0.7rem; width: 18px; height: 16px; line-height: 1;
    border-radius: 6px; transition: color .12s, background .12s
  }
  .sync-btn:hover { color: var(--text); background: var(--sf3) }
  .sync-val {
    background: none; border: none; color: var(--muted); cursor: pointer;
    font-size: 0.56rem; font-weight: 700; min-width: 26px; height: 16px;
    border-radius: 6px; font-variant-numeric: tabular-nums;
    transition: color .12s
  }
  .sync-val-on { color: var(--accent) }
  .sync-tap-on { color: var(--accent); background: var(--accent-15) }
  .sync-hint {
    flex-shrink: 0; text-align: center;
    font-size: 0.62rem; font-weight: 600; letter-spacing: 0.04em;
    color: var(--accent); background: var(--accent-08);
    border-bottom: 1px solid var(--border);
    padding: 0.35rem 0.6rem; text-transform: none
  }
  /* muted pill matching the header's uppercase-label voice */
  .unsynced-badge {
    font-size: 0.56rem; font-weight: 700; letter-spacing: 0.07em;
    color: var(--muted); border: 1px solid var(--border);
    padding: 0.12rem 0.45rem; border-radius: 10px;
    opacity: 0.85; cursor: default
  }
  .lyrics-list {
    flex: 1; overflow-y: auto; padding: 1.5rem 0;
    mask-image: linear-gradient(transparent, black 8%, black 92%, transparent);
    -webkit-mask-image: linear-gradient(transparent, black 8%, black 92%, transparent)
  }

  /* lyrics empty / not-found state */
  .lyrics-empty {
    flex: 1;
    display: flex; flex-direction: column;
    align-items: center; justify-content: center;
    gap: 0.7rem; padding: 1.5rem; text-align: center;
    color: var(--muted);
  }
  .lyrics-glyph { font-size: 2.2rem; opacity: 0.35 }
  .lyrics-empty p { font-size: 0.8rem }
  .lyrics-empty-title { font-weight: 600; color: var(--text); font-size: 0.85rem !important }
  .lyrics-empty-sub   { color: var(--muted); font-size: 0.72rem !important; max-width: 220px; line-height: 1.5 }
  .retry-btn {
    display: flex; align-items: center; gap: 0.4rem;
    background: var(--accent-08); color: var(--accent);
    border: 1px solid rgba(var(--accent-rgb), 0.4);
    padding: 0.45rem 0.9rem; border-radius: 20px;
    font-size: 0.74rem; font-weight: 600;
    cursor: pointer; margin-top: 0.4rem;
    transition: background .15s, transform .15s
  }
  .retry-btn:hover { background: var(--accent-15); transform: translateY(-1px) }
  .retry-btn:active { transform: translateY(0) }
</style>
