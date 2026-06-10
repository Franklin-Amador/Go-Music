<script lang="ts">
  import { s, d } from './stores.svelte'
  import {
    fmtTime, togglePlay, next, prev, stop,
    toggleShuffle, toggleRepeat, toggleExclusive,
    onProgressMouseDown, onVolumeInput,
  } from './player'

  const posStr = $derived(fmtTime(d.displayPos))
  const durStr = $derived(fmtTime(s.dur))
</script>

<!-- ════ RIGHT PLAYER ════════════════════════════════════════════════════ -->
<main class="player">

  <!-- ambient gradient bg driven by accent -->
  <div class="player-bg" aria-hidden="true"></div>

  <!-- album art -->
  <div class="art-section">
    {#if s.track?.artBase64}
      <div class="art-wrap">
        <img class="art-glow" src={s.track.artBase64} alt="" aria-hidden="true" />
        <img class="art-img"  src={s.track.artBase64} alt="Album art" />
      </div>
    {:else}
      <div class="art-wrap art-empty">
        <span class="art-glyph">♫</span>
      </div>
    {/if}
  </div>

  <!-- track info -->
  <div class="track-info">
    {#if s.loading}
      <div class="loading-dots"><span></span><span></span><span></span></div>
    {:else if s.track}
      <h1 class="track-title">{s.track.title}</h1>
      <p class="track-sub">
        {#if s.track.artist}<span class="track-artist">{s.track.artist}</span>{/if}
        {#if s.track.artist && s.track.album}<span class="dot">·</span>{/if}
        {#if s.track.album}<span class="track-album">{s.track.album}</span>{/if}
      </p>
      <div class="badges">
        <span class="badge">{s.track.qualityLabel || s.track.format}</span>
        {#if s.track.isDSD}<span class="badge badge-dsd">{s.track.dsdLabel}</span>{/if}
        {#if s.track.outputMode}
          <span class="badge" class:badge-excl={s.track.outputMode==='exclusive'}>
            {s.track.outputMode === 'exclusive' ? '● EXCLUSIVE' : '○ SHARED'}
          </span>
        {/if}
      </div>
    {:else}
      <p class="track-empty">Drop files here or use the sidebar</p>
    {/if}
  </div>

  <!-- waveform -->
  <canvas bind:this={s.canvasEl} class="waveform" width="640" height="72"></canvas>

  <!-- progress -->
  <div class="progress-section">
    <span class="time">{posStr}</span>
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="progress-track"
         onmousedown={onProgressMouseDown}
         class:dragging={s.dragging}>
      <div class="progress-fill" style="width:{d.progress}%">
        <div class="progress-thumb"></div>
      </div>
    </div>
    <span class="time">{durStr}</span>
  </div>

  <!-- transport -->
  <div class="transport">
    <button class="ctrl-btn" onclick={prev} title="Previous" disabled={!s.playlist.length}>
      <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 6h2v12H6zm3.5 6 8.5 6V6z"/></svg>
    </button>
    <button class="play-btn" onclick={togglePlay}
            title={d.isPlaying ? 'Pause' : 'Play'}
            disabled={!s.track && !s.playlist.length}>
      {#if d.isPlaying}
        <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/></svg>
      {:else}
        <svg viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>
      {/if}
    </button>
    <button class="ctrl-btn" onclick={next} title="Next" disabled={!s.playlist.length}>
      <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 18l8.5-6L6 6v12zm2-8.14L11.03 12 8 14.14V9.86zM16 6h2v12h-2z"/></svg>
    </button>
    <button class="ctrl-btn" onclick={stop} title="Stop"
            disabled={!d.isPlaying && !d.isPaused}>
      <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 6h12v12H6z"/></svg>
    </button>
  </div>

  <!-- toggles -->
  <div class="toggles">
    <button class="tog" class:tog-on={s.isShuffle}  onclick={toggleShuffle}>
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M10.59 9.17L5.41 4 4 5.41l5.17 5.17zm4.76-.99l3.65 3.65-3.65 3.65V13h-1.76l-6.4-6.4 1.41-1.41 5.75 5.75V9.18zm.59 10.41v-2.59l-10-10H4V4h1.41l10 10H18V11.41l3.5 3.5z"/></svg>
      Shuffle
    </button>
    <button class="tog" class:tog-on={s.isRepeat} onclick={toggleRepeat}>
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M7 7h10v3l4-4-4-4v3H5v6h2zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2z"/></svg>
      Repeat
    </button>
    <button class="tog" class:tog-on={s.showLyrics} onclick={() => s.showLyrics=!s.showLyrics}>
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M12 3v10.55A4 4 0 1 0 14 17V7h4V3z"/></svg>
      Lyrics
    </button>
    <button class="tog excl-tog" class:tog-on={s.isExclPref} onclick={toggleExclusive}>
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 14.5v-9l6 4.5-6 4.5z"/></svg>
      {s.isExclPref ? 'Exclusive' : 'Shared'}
    </button>
    <button class="tog" onclick={() => s.showNowPlaying=true} title="Immersive view"
            disabled={!s.track}>
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M7 14H5v5h5v-2H7v-3zm-2-4h2V7h3V5H5v5zm12 7h-3v2h5v-5h-2v3zM14 5v2h3v3h2V5h-5z"/></svg>
      Immersive
    </button>
    <button class="tog" onclick={() => s.showSettings=true} title="Settings">
      <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58a.49.49 0 0 0 .12-.61l-1.92-3.32a.488.488 0 0 0-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54a.484.484 0 0 0-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58a.49.49 0 0 0-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg>
      Settings
    </button>
  </div>

  <!-- volume -->
  <div class="volume-row">
    <svg class="vol-icon" viewBox="0 0 24 24" fill="currentColor" width="16" height="16">
      {#if s.volume === 0}
        <path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51C20.63 14.91 21 13.5 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06c1.38-.31 2.63-.95 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z"/>
      {:else if s.volume < 0.5}
        <path d="M18.5 12c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM5 9v6h4l5 5V4L9 9H5z"/>
      {:else}
        <path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/>
      {/if}
    </svg>
    <input type="range" min="0" max="1" step="0.01" value={s.volume}
           oninput={onVolumeInput} class="vol-slider"
           style="--vol:{s.volume}" />
    <span class="vol-pct">{Math.round(s.volume*100)}%</span>
  </div>

  {#if s.errorMsg}
    <div class="error-bar">{s.errorMsg} <button onclick={() => s.errorMsg=''}>✕</button></div>
  {/if}

</main>

<style>
  /* ════════════════ PLAYER ══════════════════════════════════════════ */
  .player {
    position: relative;
    display: flex; flex-direction: column; align-items: center;
    /* `safe center` centres the block vertically when there's spare height
       (maximised window) but falls back to top-aligned when the content is
       taller than the viewport (small window) — so nothing gets clipped above
       the scroll. */
    justify-content: safe center;
    gap: 0.7rem; padding: 1.6rem 2.4rem 1.2rem;
    overflow-y: auto;
  }

  /* ambient gradient — mucho más intenso, cubre más área */
  .player-bg {
    position: absolute; inset: 0; pointer-events: none; z-index: 0;
    transition: opacity 0.8s ease;
    background:
      radial-gradient(ellipse 110% 65% at 50% -8%,
        rgba(var(--accent-rgb), 0.28) 0%, transparent 60%),
      radial-gradient(ellipse 70% 50% at 10% 90%,
        rgba(var(--accent-rgb), 0.15) 0%, transparent 55%),
      radial-gradient(ellipse 60% 45% at 90% 85%,
        rgba(var(--accent-rgb), 0.10) 0%, transparent 50%),
      var(--bg)
  }

  /* everything above the bg */
  .player > *:not(.player-bg) { position: relative; z-index: 1 }

  /* album art — responsive al espacio disponible */
  .art-section { flex-shrink: 0 }
  .art-wrap {
    position: relative;
    /* min(vw-based, vh-based, px cap) — se adapta al tamaño de ventana */
    width: min(min(30vw, 38vh), 300px);
    aspect-ratio: 1;
    border-radius: clamp(12px, 2vw, 20px);
    overflow: hidden;
    box-shadow:
      0 30px 90px rgba(0,0,0,0.8),
      0 0 0 1px rgba(var(--accent-rgb), 0.15),
      0 0 70px rgba(var(--accent-rgb), 0.25)
  }
  .art-wrap.art-empty {
    background: var(--sf1);
    display: flex; align-items: center; justify-content: center
  }
  .art-glyph { font-size: min(5rem, 8vw); color: var(--muted2) }
  .art-img {
    width: 100%; height: 100%; object-fit: cover;
    position: relative; z-index: 1; transition: transform 0.4s ease
  }
  .art-wrap:hover .art-img { transform: scale(1.02) }
  .art-glow {
    position: absolute; inset: -30%; width: 160%; height: 160%;
    object-fit: cover;
    filter: blur(50px) saturate(3.5) brightness(0.55);
    opacity: 0.70; z-index: 0;
    /* scale pulses with --beat (no transition on transform = instant beat);
       opacity keeps its slow cross-fade on track change */
    transform: scale(calc(1 + var(--beat, 0) * 0.06));
    transition: opacity 0.8s ease
  }

  /* track info */
  .track-info { width: 100%; max-width: 640px; text-align: center }
  .track-title {
    font-size: clamp(1.15rem, 2.4vw, 1.6rem); font-weight: 700; line-height: 1.25;
    letter-spacing: -0.015em;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
    overflow: hidden
  }
  .track-sub {
    font-size: clamp(0.8rem, 1.3vw, 0.92rem); color: var(--muted); margin-top: 0.3rem;
    display: flex; gap: 0.4rem; justify-content: center; align-items: center; flex-wrap: wrap
  }
  .track-artist { color: var(--text) }
  .track-album  { font-style: italic }
  .track-empty { color: var(--muted); font-size: 0.85rem }

  /* waveform */
  .waveform { width: 100%; max-width: 640px; height: 72px; display: block; border-radius: 8px }

  /* toggles */
  .toggles { display: flex; gap: 0.4rem; flex-wrap: wrap; justify-content: center }
  .excl-tog.tog-on { border-color: #4caf50; color: #4caf50; background: #4caf5012 }

  /* error */
  .error-bar {
    width: 100%; max-width: 640px;
    background: #2a0f0f; border: 1px solid #6b1818; border-radius: var(--r);
    padding: 0.4rem 0.75rem; font-size: 0.75rem; color: #f08080;
    display: flex; justify-content: space-between; align-items: center; gap: 0.5rem
  }
  .error-bar button { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 0.8rem }
</style>
