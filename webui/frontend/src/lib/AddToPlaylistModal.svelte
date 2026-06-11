<script lang="ts">
  import { s } from './stores.svelte'
  import { addToList, addToNewList, rowKey } from './player'
</script>

<!-- ════ ADD TO PLAYLIST MENU ════════════════════════════════════════ -->
{#if s.addMenuPath !== null}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-backdrop" onclick={() => s.addMenuPath=null}>
    <div class="settings-card add-card" onclick={(e) => e.stopPropagation()}>
      <div class="settings-head">
        <span>Add to playlist</span>
        <button class="lyrics-close" onclick={() => s.addMenuPath=null}>✕</button>
      </div>
      <p class="add-song" title={s.addMenuTitle}>{s.addMenuTitle}</p>
      {#if s.playlists.length > 0}
        <ul class="add-list">
          {#each s.playlists as pl (pl.name)}
            <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
            <li class="add-row" role="button" tabindex="0"
                onclick={() => addToList(pl.name)}
                onkeydown={rowKey(() => addToList(pl.name))}>
              <span class="add-row-name">{pl.name}</span>
              <span class="add-row-count">{pl.count}</span>
            </li>
          {/each}
        </ul>
      {/if}
      <div class="set-row newlist-row add-new">
        <input class="search-input" type="text" placeholder="New playlist…"
               bind:value={s.addMenuNew}
               onkeydown={(e) => { if (e.key==='Enter') addToNewList() }} />
        <button class="mini-btn accent" onclick={addToNewList} disabled={!s.addMenuNew.trim()}>Add</button>
      </div>
    </div>
  </div>
{/if}

<style>
  /* add-to-playlist modal */
  .add-card { width: min(360px, 90vw) }
  .add-song {
    padding: 0.7rem 1.1rem 0.2rem; font-size: 0.82rem; font-weight: 600;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis
  }
  .add-list {
    list-style: none; max-height: 40vh; overflow-y: auto;
    padding: 0.3rem 0.6rem; margin: 0 0.5rem;
  }
  .add-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.5rem 0.6rem; border-radius: 7px; cursor: pointer;
    transition: background .12s
  }
  .add-row:hover { background: var(--accent-08) }
  .add-row-name { font-size: 0.8rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .add-row:hover .add-row-name { color: var(--accent) }
  .add-row-count { font-size: 0.6rem; color: var(--muted); background: var(--sf2); border-radius: 10px; padding: 0 6px; flex-shrink: 0; margin-left: 0.5rem }
  .add-new { border-top: 1px solid var(--border); margin-top: 0.2rem }
</style>
