<script lang="ts">
  import { s } from './stores.svelte'
  import {
    onCrossfadeInput, toggleExclusive, loadDevices, onSelectDevice,
    setVisualizerMode, setAccentSource,
  } from './player'
</script>

<!-- ════ SETTINGS MODAL ══════════════════════════════════════════════ -->
{#if s.showSettings}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-backdrop" onclick={() => s.showSettings=false}>
    <div class="settings-card" onclick={(e) => e.stopPropagation()}>
      <div class="settings-head">
        <span>Settings</span>
        <button class="lyrics-close" onclick={() => s.showSettings=false}>✕</button>
      </div>

      <div class="set-row">
        <span class="set-label">Crossfade <span class="set-val">{s.crossfade > 0 ? s.crossfade.toFixed(1) + 's' : 'Off'}</span></span>
        <input type="range" min="0" max="12" step="0.5" value={s.crossfade}
               oninput={onCrossfadeInput} class="vol-slider set-slider"
               style="--vol:{s.crossfade/12}" />
      </div>

      <div class="set-row set-row-flex">
        <span class="set-label">Exclusive output (WASAPI)</span>
        <button class="tog" class:tog-on={s.isExclPref} onclick={toggleExclusive}>
          {s.isExclPref ? 'On' : 'Off'}
        </button>
      </div>

      <div class="set-row">
        <span class="set-label">
          Output device
          <button class="set-refresh" title="Refresh devices" onclick={loadDevices}>↻</button>
        </span>
        <select class="dev-select" value={s.selectedDevice} onchange={onSelectDevice}>
          <option value="">System default</option>
          {#each s.outputDevices as dev (dev.id)}
            <option value={dev.id}>{dev.name}{dev.isDefault ? ' · default' : ''}</option>
          {/each}
        </select>
        <span class="set-hint">
          {s.devicesLoading ? 'Detecting devices…' : 'Applies in exclusive mode, on the next track. Shared mode always uses the Windows default.'}
        </span>
      </div>

      <div class="set-row">
        <span class="set-label">Visualizer</span>
        <div class="seg">
          <button class:seg-on={s.visualizerMode==='bars'}   onclick={() => setVisualizerMode('bars')}>Bars</button>
          <button class:seg-on={s.visualizerMode==='wave'}   onclick={() => setVisualizerMode('wave')}>Wave</button>
          <button class:seg-on={s.visualizerMode==='radial'} onclick={() => setVisualizerMode('radial')}>Radial</button>
        </div>
      </div>

      <div class="set-row">
        <span class="set-label">Accent colour</span>
        <div class="seg">
          <button class:seg-on={s.accentSource==='auto'}  onclick={() => setAccentSource('auto')}>From art</button>
          <button class:seg-on={s.accentSource==='fixed'} onclick={() => setAccentSource('fixed')}>Fixed</button>
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  .set-row-flex { flex-direction: row; align-items: center; justify-content: space-between }
  .set-label {
    font-size: 0.8rem; color: var(--text); font-weight: 500;
    display: flex; align-items: center; gap: 0.5rem
  }
  .set-val {
    font-size: 0.72rem; color: var(--accent); font-variant-numeric: tabular-nums;
    font-weight: 600
  }
  .set-slider { flex: none; width: 100% }
  .set-hint { font-size: 0.66rem; color: var(--muted); line-height: 1.4 }
  .set-refresh {
    background: none; border: none; color: var(--muted); cursor: pointer;
    font-size: 0.85rem; padding: 0 2px; border-radius: 4px; transition: color .12s, transform .3s
  }
  .set-refresh:hover { color: var(--accent); transform: rotate(180deg) }
  .dev-select {
    width: 100%; background: var(--sf2); border: 1px solid var(--border);
    color: var(--text); font-size: 0.76rem; padding: 0.4rem 0.5rem;
    border-radius: 7px; outline: none; cursor: pointer; transition: border-color .15s
  }
  .dev-select:focus { border-color: var(--accent) }
  .dev-select option { background: var(--sf1); color: var(--text) }

  /* segmented control */
  .seg {
    display: flex; gap: 3px; background: var(--sf2);
    border: 1px solid var(--border); border-radius: 9px; padding: 3px
  }
  .seg button {
    flex: 1; background: none; border: none; color: var(--muted);
    font-size: 0.72rem; font-weight: 600; padding: 0.4rem 0;
    border-radius: 6px; cursor: pointer; transition: background .15s, color .15s
  }
  .seg button:hover  { color: var(--text) }
  .seg button.seg-on { background: var(--accent-15); color: var(--accent) }
</style>
