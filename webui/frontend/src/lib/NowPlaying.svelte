<script lang="ts">
  import { fade, scale } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { s, d } from './stores.svelte'
  import {
    fmtTime, togglePlay, next, prev,
    toggleShuffle, toggleRepeat,
    onProgressMouseDown, onVolumeInput,
  } from './player'
  import LyricLines from './LyricLines.svelte'

  const posStr = $derived(fmtTime(d.displayPos))
  const durStr = $derived(fmtTime(s.dur))
</script>

<!-- ════ NOW PLAYING (immersive) ═════════════════════════════════════ -->
{#if s.showNowPlaying}
  <section class="now-playing"
           class:np-has-lyrics={s.lyrics.length > 0 && s.npLyricsVisible}
           class:np-idle={s.npIdle}
           transition:fade={{ duration: 240 }}>
    {#if s.track?.artBase64}
      <img class="np-bg" src={s.track.artBase64} alt="" aria-hidden="true" />
    {/if}
    <div class="np-scrim" aria-hidden="true"></div>

    <button class="np-close np-chrome" onclick={() => s.showNowPlaying=false} title="Close (Esc)">✕</button>

    <div class="np-stage">
     <div class="np-main">
      <div class="np-art-wrap" in:scale={{ duration: 420, start: 0.92, easing: cubicOut }}>
        {#if s.track?.artBase64}
          <img class="np-art" src={s.track.artBase64} alt="Album art" />
        {:else}
          <div class="np-art np-art-empty"><span>♫</span></div>
        {/if}
      </div>

      <div class="np-info">
        <h1 class="np-title">{s.track?.title ?? 'Nothing playing'}</h1>
        <p class="np-sub">
          {#if s.track?.artist}<span class="np-artist">{s.track.artist}</span>{/if}
          {#if s.track?.artist && s.track?.album}<span class="dot">·</span>{/if}
          {#if s.track?.album}<span class="np-album">{s.track.album}</span>{/if}
        </p>

        {#if s.track}
          <div class="badges np-badges np-chrome">
            <span class="badge">{s.track.qualityLabel || s.track.format}</span>
            {#if s.track.isDSD}<span class="badge badge-dsd">{s.track.dsdLabel}</span>{/if}
            {#if s.track.outputMode}
              <span class="badge" class:badge-excl={s.track.outputMode==='exclusive'}>
                {s.track.outputMode === 'exclusive' ? '● EXCLUSIVE' : '○ SHARED'}
              </span>
            {/if}
          </div>
        {/if}

        <canvas bind:this={s.npCanvasEl} class="np-wave" width="1100" height="150"></canvas>

        <div class="np-chrome np-controls">
          <div class="progress-section np-progress">
            <span class="time">{posStr}</span>
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div class="progress-track" onmousedown={onProgressMouseDown} class:dragging={s.dragging}>
              <div class="progress-fill" style="width:{d.progress}%"><div class="progress-thumb"></div></div>
            </div>
            <span class="time">{durStr}</span>
          </div>

          <div class="transport np-transport">
            <button class="ctrl-btn" onclick={prev} title="Previous" disabled={!s.playlist.length}>
              <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 6h2v12H6zm3.5 6 8.5 6V6z"/></svg>
            </button>
            <button class="play-btn" onclick={togglePlay} title={d.isPlaying ? 'Pause' : 'Play'} disabled={!s.track && !s.playlist.length}>
              {#if d.isPlaying}
                <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/></svg>
              {:else}
                <svg viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>
              {/if}
            </button>
            <button class="ctrl-btn" onclick={next} title="Next" disabled={!s.playlist.length}>
              <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 18l8.5-6L6 6v12zm10-12h2v12h-2z"/></svg>
            </button>
          </div>

          <div class="np-secondary">
            <button class="tog" class:tog-on={s.isShuffle} onclick={toggleShuffle} title="Shuffle">
              <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M10.59 9.17L5.41 4 4 5.41l5.17 5.17zm4.76-.99l3.65 3.65-3.65 3.65V13h-1.76l-6.4-6.4 1.41-1.41 5.75 5.75V9.18zm.59 10.41v-2.59l-10-10H4V4h1.41l10 10H18V11.41l3.5 3.5z"/></svg>
            </button>
            <button class="tog" class:tog-on={s.isRepeat} onclick={toggleRepeat} title="Repeat">
              <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M7 7h10v3l4-4-4-4v3H5v6h2zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2z"/></svg>
            </button>
            {#if s.lyrics.length > 0}
              <button class="tog" class:tog-on={s.npLyricsVisible} onclick={() => s.npLyricsVisible=!s.npLyricsVisible} title="Lyrics">
                <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M12 3v10.55A4 4 0 1 0 14 17V7h4V3z"/></svg>
              </button>
            {/if}
          </div>

          <div class="volume-row np-volume">
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
                   oninput={onVolumeInput} class="vol-slider" style="--vol:{s.volume}" />
            <span class="vol-pct">{Math.round(s.volume*100)}%</span>
          </div>
        </div>
      </div>
     </div><!-- /np-main -->

      {#if s.lyrics.length > 0 && s.npLyricsVisible}
        <div class="np-lyrics">
          {#if !d.lyricsSynced}
            <div class="np-unsynced">Unsynced</div>
          {/if}
          <LyricLines big={true} />
        </div>
      {/if}
    </div>

    {#if s.upNext}
      <button class="np-upnext np-chrome" onclick={next} title="Play next">
        <svg viewBox="0 0 24 24" fill="currentColor" width="13" height="13"><path d="M6 18l8.5-6L6 6v12zm10-12h2v12h-2z"/></svg>
        <span class="np-upnext-text">
          <span class="np-upnext-label">Up next</span>
          <span class="np-upnext-title">{s.upNext.title}</span>
        </span>
      </button>
    {/if}
  </section>
{/if}

<style>
  /* ════════════════ NOW PLAYING (immersive) ═════════════════════════ */
  .now-playing {
    position: fixed; inset: 0; z-index: 40; overflow: hidden;
    background: var(--bg);
    animation: fade-in .25s ease-out
  }
  @keyframes fade-in { from { opacity: 0 } to { opacity: 1 } }
  .np-bg {
    position: absolute; inset: -8%; width: 116%; height: 116%;
    object-fit: cover;
    filter: blur(70px) saturate(2.4) brightness(0.45);
    transform: scale(1.1); z-index: 0
  }
  .np-scrim {
    position: absolute; inset: 0; z-index: 1;
    background:
      radial-gradient(ellipse 80% 60% at 50% 40%, transparent 0%, rgba(0,0,0,0.45) 100%),
      linear-gradient(180deg, rgba(10,10,12,0.35) 0%, rgba(10,10,12,0.75) 100%)
  }
  .np-close {
    position: absolute; top: 1rem; right: 1.2rem; z-index: 4;
    background: rgba(255,255,255,0.06); border: 1px solid rgba(255,255,255,0.10);
    color: var(--text); width: 36px; height: 36px; border-radius: 50%;
    font-size: 0.9rem; cursor: pointer; transition: background .15s, transform .15s, opacity .45s
  }
  .np-close:hover { background: rgba(255,255,255,0.14); transform: scale(1.06) }

  .np-stage {
    position: relative; z-index: 2; height: 100%;
    display: flex; align-items: center; justify-content: center;
    gap: clamp(1.5rem, 5vw, 5rem);
    padding: clamp(1.5rem, 4vh, 3.5rem) clamp(1.5rem, 5vw, 4rem)
  }
  .np-main {
    display: flex; flex-direction: column; align-items: center;
    gap: clamp(1rem, 2.5vh, 2rem); min-width: 0;
    max-width: 560px; flex: 1 1 auto
  }
  .np-art-wrap {
    width: min(42vh, 38vw, 440px); aspect-ratio: 1; flex-shrink: 0;
    border-radius: clamp(14px, 2vw, 24px); overflow: hidden;
    box-shadow: 0 40px 110px rgba(0,0,0,0.7), 0 0 0 1px rgba(var(--accent-rgb),0.18),
                0 0 90px rgba(var(--accent-rgb),0.30)
  }
  .np-art { width: 100%; height: 100%; object-fit: cover; display: block;
            transform: scale(calc(1 + var(--beat, 0) * 0.02)) }
  .np-art-empty {
    background: var(--sf1); display: flex; align-items: center; justify-content: center;
    font-size: 5rem; color: var(--muted2)
  }
  .np-info { width: 100%; max-width: 520px; text-align: center }
  .np-title {
    font-size: clamp(1.5rem, 3vw, 2.3rem); font-weight: 800; line-height: 1.15;
    letter-spacing: -0.02em;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden
  }
  .np-sub {
    font-size: clamp(0.9rem, 1.5vw, 1.1rem); color: var(--muted); margin-top: 0.5rem;
    display: flex; gap: 0.5rem; justify-content: center; align-items: center; flex-wrap: wrap
  }
  .np-artist { color: var(--text) }
  .np-album  { font-style: italic }
  .np-wave { width: 100%; height: clamp(70px, 12vh, 130px); display: block; margin: 0.4rem 0 }
  .np-progress { max-width: none; margin-top: 0.2rem }
  .np-transport { margin-top: 0.4rem; gap: 1rem }
  .np-transport .play-btn { width: 62px; height: 62px }
  .np-transport .play-btn svg { width: 28px; height: 28px }

  .np-lyrics {
    flex: 1 1 0; min-width: 0; max-width: 460px; height: 80%;
    overflow-y: auto; padding: 2rem 0;
    mask-image: linear-gradient(transparent, black 10%, black 90%, transparent);
    -webkit-mask-image: linear-gradient(transparent, black 10%, black 90%, transparent)
  }
  /* Discreet "plain lyrics" hint at the top of the immersive lyrics column */
  .np-unsynced {
    text-align: center; margin-bottom: 0.8rem;
    font-size: 0.58rem; font-weight: 700; letter-spacing: 0.09em;
    text-transform: uppercase; color: var(--muted); opacity: 0.7
  }
  /* Without lyrics the main column simply centers; with lyrics it sits left. */
  .now-playing:not(.np-has-lyrics) .np-stage { flex-direction: column }

  .np-badges { margin-top: 0.7rem; justify-content: center }
  .np-controls {
    display: flex; flex-direction: column; align-items: center;
    gap: 0.7rem; width: 100%; margin-top: 0.7rem
  }
  .np-secondary { display: flex; gap: 0.5rem; justify-content: center }
  .np-secondary .tog {   /* icon-only: true 32px squares, icon centered */
    width: 32px; height: 32px; padding: 0; justify-content: center
  }
  .np-volume { max-width: 300px; margin: 0 auto }

  /* "Up next" floating card */
  .np-upnext {
    position: absolute; bottom: 1.4rem; right: 1.6rem; z-index: 3;
    display: flex; align-items: center; gap: 0.6rem;
    max-width: 260px; padding: 0.55rem 0.85rem;
    background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.10);
    border-radius: 12px; cursor: pointer; color: var(--text); text-align: left;
    backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px);
    transition: background .15s, transform .15s, opacity .45s
  }
  .np-upnext:hover { background: rgba(255,255,255,0.12); transform: translateY(-2px) }
  .np-upnext svg   { color: var(--accent); flex-shrink: 0 }
  .np-upnext-text  { display: flex; flex-direction: column; min-width: 0 }
  .np-upnext-label {
    font-size: 0.56rem; text-transform: uppercase; letter-spacing: 0.07em;
    color: var(--muted); font-weight: 700
  }
  .np-upnext-title {
    font-size: 0.8rem; font-weight: 600;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis
  }

  /* Auto-hide: chrome fades out and the cursor disappears after inactivity.
     Art, title, visualizer and lyrics stay (they carry no .np-chrome class). */
  .np-chrome { transition: opacity .45s ease }
  .now-playing.np-idle { cursor: none }
  .now-playing.np-idle .np-chrome { opacity: 0; pointer-events: none }

  /* Narrow windows: drop the lyrics column, stack vertically */
  @media (max-width: 900px) {
    .np-lyrics { display: none }
    .np-stage  { flex-direction: column }
    .np-upnext { display: none }
  }
</style>
