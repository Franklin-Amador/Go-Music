<script lang="ts">
  import { s, d } from './stores.svelte'
  import { retryLyrics } from './player'
  import LyricLines from './LyricLines.svelte'
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
        <button class="lyrics-close" onclick={() => s.showLyrics=false}>✕</button>
      </span>
    </div>

    {#if s.lyrics.length > 0}
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
