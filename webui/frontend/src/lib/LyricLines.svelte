<script lang="ts">
  // Reusable depth-of-field lyric list (shared by the side panel + Now Playing).
  // `big` scales the type up for the immersive view.
  import { s, d } from './stores.svelte'

  let { big }: { big: boolean } = $props()

  // Memoise so the list only re-renders when the active index actually changes,
  // not on every raw position tick.
  const activeLyricIdx = $derived(d.activeLyricIdx)
  const synced = $derived(d.lyricsSynced)
</script>

{#if synced}
  {#each s.lyrics as line, i}
    {@const dist  = activeLyricIdx >= 0 ? Math.abs(i - activeLyricIdx) : 8}
    {@const sizes = big ? ['1.7rem','1.18rem','0.98rem','0.84rem','0.76rem']
                        : ['1.10rem','0.90rem','0.78rem','0.70rem','0.65rem']}
    {@const opas   = [1, 0.70, 0.42, 0.22, 0.12]}
    {@const scales = [1.04, 0.97, 0.93, 0.90, 0.87]}
    {@const blurs  = [0, 0.6, 1.4, 2.4, 3.2]}
    {@const dd     = Math.min(dist, 4)}
    <p class="lyric"
       class:lyric-active={dist === 0}
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
      {line.text || ' '}
    </p>
  {/each}
{/if}

<style>
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
