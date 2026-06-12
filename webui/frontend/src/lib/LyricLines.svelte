<script lang="ts">
  // Reusable depth-of-field lyric list (shared by the side panel + Now Playing).
  // `big` scales the type up for the immersive view.
  //
  // Synced lyrics are animated by the lyric flow engine (lyricflow.svelte.ts):
  // it owns a rAF loop that writes opacity / scale / blur straight to these
  // elements and the accent colour to the active line's <span> — no Svelte
  // reactivity per frame. This template only renders the static skeleton (fixed
  // font-size, fixed weight — nothing here ever reflows) and registers the
  // elements with the engine.
  //
  // Under prefers-reduced-motion the old static bucket renderer is used
  // instead: discrete distance tiers, no drift, no sweep, no spring scroll
  // (App.svelte jump-scrolls the active line into view).
  import { s, d } from './stores.svelte'
  import { registerLyricLines, reduceMotion, livePosNow } from './lyricflow.svelte'
  import { calibrateLyricToLine } from './player'

  let { big }: { big: boolean } = $props()

  const activeLyricIdx = $derived(d.activeLyricIdx)   // reduced-motion renderer only
  const synced = $derived(d.lyricsSynced)
  const flow   = $derived(synced && !reduceMotion)

  // Line elements in lyric order (plain array — only the engine reads it).
  let lineEls: HTMLElement[] = []

  // The per-track timing correction shifts every TIMED line; untimed (-1)
  // lines keep their sentinel. Reading s.lyricOffset here makes the effect
  // re-register (and the engine re-flow) whenever the user calibrates.
  const shiftedTimes = $derived(s.lyrics.map(l => l.timeSec < 0 ? -1 : l.timeSec + s.lyricOffset))

  // (Re)register with the engine whenever the lyrics change. The parent of
  // the lines is the scrollable surface (.lyrics-list / .np-lyrics).
  $effect(() => {
    if (!flow) return
    const els = lineEls.slice(0, s.lyrics.length).filter((el): el is HTMLElement => !!el)
    const container = els[0]?.parentElement
    if (!container) return
    return registerLyricLines(big, els, container, shiftedTimes)
  })

  // Tap-to-sync: in calibration mode a click on a line means "I am hearing
  // THIS line right now" — the offset follows from the interpolated clock.
  function onLineClick(timeSec: number) {
    if (!s.lyricSyncMode || timeSec < 0) return
    calibrateLyricToLine(timeSec, livePosNow())
  }
</script>

{#if flow}
  <!-- Flow renderer: the engine styles a ±6-line window each frame; the
       default CSS below is the "far" look so unstyled lines never flash. -->
  {#each s.lyrics as line, i}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <p bind:this={lineEls[i]}
       class="lyric-fl" class:lyric-fl-big={big} class:lyric-syncable={s.lyricSyncMode && line.timeSec >= 0}
       onclick={() => onLineClick(line.timeSec)}><span>{line.text || '·'}</span></p>
  {/each}
{:else if synced}
  {#each s.lyrics as line, i}
    {@const dist  = activeLyricIdx >= 0 ? Math.abs(i - activeLyricIdx) : 8}
    {@const sizes = big ? ['1.7rem','1.18rem','0.98rem','0.84rem','0.76rem']
                        : ['1.10rem','0.90rem','0.78rem','0.70rem','0.65rem']}
    {@const opas   = [1, 0.70, 0.42, 0.22, 0.12]}
    {@const scales = [1.04, 0.97, 0.93, 0.90, 0.87]}
    {@const blurs  = [0, 0.6, 1.4, 2.4, 3.2]}
    {@const dd     = Math.min(dist, 4)}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <p class="lyric"
       class:lyric-active={dist === 0}
       class:lyric-syncable={s.lyricSyncMode && line.timeSec >= 0}
       onclick={() => onLineClick(line.timeSec)}
       style="font-size:{sizes[dd]};opacity:{opas[dd]};transform:scale({scales[dd]});filter:blur({blurs[dd]}px);">
      {line.text || '·'}
    </p>
  {/each}
{:else}
  <!-- Unsynced (plain) lyrics: no active line exists, so the DoF treatment
       would push every line to max distance (tiny + blurred). Render a flat,
       readable list instead. Empty lines stay as visual stanza breaks. -->
  {#each s.lyrics as line}
    <p class="lyric lyric-plain" style="font-size:{big ? '1.18rem' : '0.90rem'};">
      {line.text || ' '}
    </p>
  {/each}
{/if}

<style>
  /* ── flow renderer (synced, full motion) ───────────────────────────── */
  .lyric-fl {
    /* Fixed font-size (the old d=0 size) — the size hierarchy is pure
       transform: scale() from the engine, so nothing ever reflows. The
       constant padding replaces the old per-tier breathing room. */
    font-size: 1.10rem;
    color: var(--text);
    text-align: center;
    padding: 0.30rem 1.4rem;
    line-height: 1.7;
    /* Constant weight: bolding only the active line would re-wrap text and
       invalidate the engine's cached line geometry. */
    font-weight: 600;
    transform-origin: center center;
    cursor: default;
    /* far-style defaults until the engine's first pass — no bright flash */
    opacity: 0.12;
    /* NO transitions: the engine writes fresh values every frame; a CSS
       tween on top would lag one frame behind and smear the motion. */
  }
  .lyric-fl-big { font-size: 1.7rem; padding: 0.34rem 1.4rem }
  /* The engine swaps the active line's <span> colour to var(--accent) on
     handover; this tween cross-fades the highlight softly instead of snapping.
     Only colour transitions — opacity/scale/blur are written fresh each frame. */
  .lyric-fl span { transition: color 0.28s ease }

  /* Calibration mode: lines become tap targets ("click the one you hear"). */
  .lyric-syncable { cursor: pointer }
  .lyric-syncable:hover { text-decoration: underline; text-decoration-color: var(--accent-50) }

  /* ── bucket renderer (reduced-motion) + unsynced list ──────────────── */
  .lyric {
    /* font-size, opacity, transform, filter vienen del inline style  */
    color: var(--text);
    text-align: center;
    padding: 0.26rem 1.4rem;
    line-height: 1.85;
    transform-origin: center center;
    cursor: default;
    transition:
      font-size  0.38s cubic-bezier(0.4, 0, 0.2, 1),
      opacity    0.38s cubic-bezier(0.4, 0, 0.2, 1),
      transform  0.38s cubic-bezier(0.4, 0, 0.2, 1),
      filter     0.38s cubic-bezier(0.4, 0, 0.2, 1),
      color      0.30s ease,
      text-shadow 0.30s ease;
  }
  .lyric-plain {
    /* static readable tier (matches the d===1 look, minus blur/scale) */
    opacity: 0.8;
    line-height: 1.7;
    padding: 0.16rem 1.4rem;
    transition: none;
  }
  .lyric-active {
    font-weight: 700;
    color: var(--accent);
    text-shadow:
      0 0 30px rgba(var(--accent-rgb), 0.55),
      0 0 60px rgba(var(--accent-rgb), 0.20);
    padding: 0.38rem 1.4rem;
  }
</style>
