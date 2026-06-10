<script lang="ts">
  import * as Backend from '../../wailsjs/go/main/App.js'
  import { s, d } from './stores.svelte'
  import {
    openFile, openFolder, scanLibraryDialog, playAt,
    onQueueDragStart, onQueueDragOver, onQueueDrop, onQueueDragEnd,
    openList, playList, queueList, deleteList, createList, removeFromList,
    openAddMenu, loadAlbum, playSongs, filterByArtist, clearArtistFilter,
  } from './player'
</script>

<!-- ════ LEFT SIDEBAR ════════════════════════════════════════════════════ -->
<aside class="sidebar">

  <div class="tabs">
    <button class="tab" class:active={s.tab==='playlist'} onclick={() => s.tab='playlist'}>Queue</button>
    <button class="tab" class:active={s.tab==='lists'}    onclick={() => s.tab='lists'}>Lists</button>
    <button class="tab" class:active={s.tab==='songs'}    onclick={() => s.tab='songs'}>Songs</button>
    <button class="tab" class:active={s.tab==='albums'}   onclick={() => s.tab='albums'}>Albums</button>
    <button class="tab" class:active={s.tab==='artists'}  onclick={() => s.tab='artists'}>Artists</button>
  </div>

  <div class="tab-content">

    <!-- QUEUE -->
    {#if s.tab === 'playlist'}
      {#if s.playlist.length === 0}
        <div class="empty-state">
          <span class="empty-icon">♫</span>
          <p>Open a file or folder to start</p>
        </div>
      {:else}
        <div class="queue-head">
          <span class="queue-count">{s.playlist.length} {s.playlist.length === 1 ? 'track' : 'tracks'}</span>
        </div>
        {#if s.playlist.length > 6}
          <div class="search-wrap">
            <input class="search-input" type="text" placeholder="Filter queue…" bind:value={s.queueSearch} />
          </div>
        {/if}
        <ul class="pl-list">
          {#each d.filteredPlaylist as t (t.index)}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <li class="pl-item"
                class:pl-current={t.current}
                class:pl-dragging={s.dragIndex === t.index}
                class:pl-dragover={s.dragOverIndex === t.index && s.dragIndex !== t.index}
                draggable={!s.queueSearch}
                ondragstart={(e) => onQueueDragStart(e, t.index)}
                ondragover={(e) => onQueueDragOver(e, t.index)}
                ondrop={(e) => onQueueDrop(e, t.index)}
                ondragend={onQueueDragEnd}
                onclick={() => playAt(t.index)}>
              <span class="pl-num">{t.current ? '▶' : t.index+1}</span>
              <span class="pl-title">{t.title}</span>
              <button class="row-add pl-add" title="Add to playlist"
                      onclick={(e) => { e.stopPropagation(); openAddMenu(t.path, t.title) }}>+</button>
              <span class="pl-remove"
                    onclick={(e) => { e.stopPropagation(); Backend.Remove(t.index) }}
                    title="Remove">✕</span>
            </li>
          {/each}
          {#if d.filteredPlaylist.length === 0}
            <li class="pl-empty">No matches</li>
          {/if}
        </ul>
      {/if}

    <!-- LISTS -->
    {:else if s.tab === 'lists'}
      <div class="queue-head">
        <span class="queue-count">{s.playlists.length} {s.playlists.length === 1 ? 'playlist' : 'playlists'}</span>
        <button class="mini-btn" onclick={() => { s.creatingList = true }}>+ New</button>
      </div>
      {#if s.creatingList}
        <div class="search-wrap newlist-row">
          <input class="search-input" type="text" placeholder="Playlist name…"
                 bind:value={s.newListName}
                 onkeydown={(e) => { if (e.key==='Enter') createList(); if (e.key==='Escape') { s.creatingList=false; s.newListName='' } }} />
          <button class="mini-btn accent" onclick={createList}>Create</button>
        </div>
      {/if}
      {#if s.playlists.length === 0 && !s.creatingList}
        <div class="empty-state">
          <span class="empty-icon">☰</span>
          <p>No playlists yet — create one, then add songs with +</p>
        </div>
      {:else}
        <ul class="named-list">
          {#each s.playlists as pl (pl.name)}
            <li class="named-item" class:named-open={s.openListName === pl.name}>
              <div class="named-row">
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <span class="named-name" onclick={() => openList(pl.name)}>
                  <span class="named-chevron">{s.openListName === pl.name ? '▾' : '▸'}</span>
                  <span class="named-title">{pl.name}</span>
                  <span class="named-count">{pl.count}</span>
                </span>
                <span class="named-actions">
                  <button title="Play" onclick={() => playList(pl.name)} disabled={pl.count===0}>▶</button>
                  <button title="Add to queue" onclick={() => queueList(pl.name)} disabled={pl.count===0}>＋</button>
                  <button title="Delete" class="named-del" onclick={() => deleteList(pl.name)}>✕</button>
                </span>
              </div>
              {#if s.openListName === pl.name}
                <ul class="named-tracks">
                  {#each s.openListTracks as t, i (t.path)}
                    <!-- svelte-ignore a11y_click_events_have_key_events -->
                    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                    <li class="named-track" onclick={() => playSongs(s.openListTracks.map(x => x.path), i)}>
                      <span class="named-track-title">{t.title}</span>
                      <span class="named-track-rm"
                            onclick={(e) => { e.stopPropagation(); removeFromList(pl.name, t.path) }}
                            title="Remove">✕</span>
                    </li>
                  {/each}
                  {#if s.openListTracks.length === 0}
                    <li class="pl-empty">Empty — add songs with +</li>
                  {/if}
                </ul>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}

    <!-- SONGS -->
    {:else if s.tab === 'songs'}
      <div class="search-wrap">
        <input class="search-input" type="text" placeholder="Search songs…"
               bind:value={s.songSearch} />
      </div>
      {#if s.songs.length === 0}
        <div class="empty-state">
          <span class="empty-icon">♪</span>
          <p>{s.songsLoaded ? 'Scan library to populate' : 'Loading…'}</p>
        </div>
      {:else}
        <p class="list-count">{d.filteredSongs.length} of {s.songs.length} songs</p>
        <ul class="song-list">
          {#each d.filteredSongs as song, i}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <li class="song-item" onclick={() => playSongs(d.filteredSongs.map(x => x.path), i)}>
              <div class="song-main">
                <span class="song-title">{song.title}</span>
                <span class="song-sub">{song.artist}{song.album ? ' · ' + song.album : ''}</span>
              </div>
              <button class="row-add" title="Add to playlist"
                      onclick={(e) => { e.stopPropagation(); openAddMenu(song.path, song.title) }}>+</button>
              <span class="song-play-icon">▶</span>
            </li>
          {/each}
        </ul>
      {/if}

    <!-- ALBUMS -->
    {:else if s.tab === 'albums'}
      {#if s.artistFilter}
        <div class="filter-bar">
          <span class="filter-lbl">{s.artistFilter}</span>
          <button class="filter-clear" onclick={clearArtistFilter}>✕</button>
        </div>
      {/if}
      {#if d.filteredAlbums.length === 0}
        <div class="empty-state">
          <span class="empty-icon">◎</span>
          <p>{s.albums.length === 0 ? 'Scan library first' : 'No albums for this artist'}</p>
        </div>
      {:else}
        <div class="album-grid">
          {#each d.filteredAlbums as al}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div class="album-card" onclick={() => loadAlbum(al.artist, al.title)}
                 style={al.accentHex ? `--card-accent:${al.accentHex}` : ''}>
              {#if al.artBase64}
                <img class="album-art" src={al.artBase64} alt={al.title} />
              {:else}
                <div class="album-placeholder">♪</div>
              {/if}
              <div class="album-meta">
                <p class="album-title">{al.title}</p>
                <p class="album-artist">{al.artist}</p>
              </div>
            </div>
          {/each}
        </div>
      {/if}

    <!-- ARTISTS -->
    {:else}
      {#if s.artists.length === 0}
        <div class="empty-state">
          <span class="empty-icon">♫</span>
          <p>Scan library first</p>
        </div>
      {:else}
        <p class="list-count">{s.artists.length} artists</p>
        <ul class="artist-list">
          {#each s.artists as artist}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <li class="artist-item" class:artist-active={artist===s.artistFilter}
                onclick={() => filterByArtist(artist)}>
              <span class="artist-name">{artist}</span>
              <span class="artist-arrow">›</span>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}

  </div><!-- /tab-content -->

  <!-- scan progress -->
  {#if s.scanning}
    <div class="scan-bar-wrap">
      <span class="scan-label">Scanning…{s.scanProgress ? ` ${s.scanProgress.done}/${s.scanProgress.total}` : ''}</span>
      {#if s.scanProgress && s.scanProgress.total > 0}
        <div class="scan-track">
          <div class="scan-fill" style="width:{(s.scanProgress.done/s.scanProgress.total)*100}%"></div>
        </div>
      {/if}
    </div>
  {/if}

  <!-- footer -->
  <div class="sidebar-footer">
    <button class="foot-btn" onclick={openFile}   title="Open audio file">+ File</button>
    <button class="foot-btn" onclick={openFolder} title="Add folder to queue">+ Folder</button>
    <button class="foot-btn accent" onclick={scanLibraryDialog} title="Scan music library">⟳ Library</button>
    {#if s.playlist.length > 0}
      <button class="foot-btn danger" onclick={() => Backend.ClearPlaylist()}>✕</button>
    {/if}
  </div>
</aside>

<style>
  /* ════════════════ SIDEBAR ═════════════════════════════════════════ */
  .sidebar {
    display: flex; flex-direction: column;
    background: linear-gradient(
      175deg,
      rgba(var(--accent-rgb), 0.05) 0%,
      var(--sf1) 18%
    );
    border-right: 1px solid var(--border);
    overflow: hidden;
    transition: background 0.6s ease;
  }

  /* tabs */
  .tabs {
    display: grid; grid-template-columns: repeat(5, 1fr);
    border-bottom: 1px solid var(--border); flex-shrink: 0
  }
  .tab {
    background: none; border: none; color: var(--muted);
    font-size: 0.62rem; font-weight: 700; letter-spacing: 0.03em;
    text-transform: uppercase; padding: 0.65rem 0; cursor: pointer;
    border-bottom: 2px solid transparent; margin-bottom: -1px;
    transition: color .15s
  }
  .tab:hover  { color: var(--text) }
  .tab.active { color: var(--accent); border-bottom-color: var(--accent) }

  /* content area */
  .tab-content { flex: 1; overflow-y: auto; overflow-x: hidden }

  /* empty state */
  .empty-state {
    display: flex; flex-direction: column; align-items: center;
    padding: 3rem 1rem; gap: 0.5rem; color: var(--muted)
  }
  .empty-icon { font-size: 2rem; opacity: 0.3 }
  .empty-state p { font-size: 0.78rem; text-align: center }

  /* count label */
  .list-count { font-size: 0.65rem; color: var(--muted); padding: 0.5rem 0.75rem }

  /* queue/playlist */
  .pl-list { list-style: none }
  .pl-item {
    display: grid; grid-template-columns: 1.8rem 1fr 1.1rem 1.1rem;
    align-items: center; gap: 0.25rem;
    padding: 0.42rem 0.6rem; cursor: pointer;
    transition: background .1s; border-left: 2px solid transparent
  }
  .pl-item:hover          { background: var(--sf2) }
  .pl-item.pl-current     { background: var(--accent-08); border-left-color: var(--accent) }
  .pl-num  { font-size: 0.62rem; color: var(--muted); text-align: right; font-variant-numeric: tabular-nums }
  .pl-current .pl-num   { color: var(--accent); font-weight: 700 }
  .pl-title { font-size: 0.77rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .pl-current .pl-title { color: var(--accent) }
  .pl-remove { font-size: 0.58rem; color: transparent; cursor: pointer; text-align: center; transition: color .1s }
  .pl-item:hover .pl-remove { color: var(--muted2) }
  .pl-remove:hover          { color: #f08080 !important }

  /* queue header + drag-reorder */
  .queue-head {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.5rem 0.75rem 0.35rem
  }
  .queue-count { font-size: 0.65rem; color: var(--muted); font-weight: 600; letter-spacing: 0.03em }
  .pl-item[draggable="true"] { cursor: grab }
  .pl-item.pl-dragging  { opacity: 0.4 }
  .pl-item.pl-dragover  { box-shadow: inset 0 2px 0 var(--accent); background: var(--accent-08) }
  .pl-empty { padding: 1.2rem 0.75rem; text-align: center; font-size: 0.72rem; color: var(--muted) }

  /* "+ add to playlist" button shared by song + queue rows */
  .row-add {
    background: none; border: none; color: transparent; cursor: pointer;
    font-size: 0.95rem; line-height: 1; padding: 0; text-align: center;
    transition: color .12s, transform .12s
  }
  .song-item:hover .row-add,
  .pl-item:hover   .row-add { color: var(--muted2) }
  .row-add:hover { color: var(--accent) !important; transform: scale(1.2) }
  .pl-add { font-size: 0.85rem }

  /* named playlists list */
  .named-list { list-style: none }
  .named-item { border-bottom: 1px solid var(--border) }
  .named-item.named-open { background: var(--sf1) }
  .named-row {
    display: flex; align-items: center; gap: 0.3rem;
    padding: 0.15rem 0.4rem 0.15rem 0.55rem
  }
  .named-name {
    flex: 1; min-width: 0; display: flex; align-items: center; gap: 0.4rem;
    cursor: pointer; padding: 0.35rem 0
  }
  .named-chevron { font-size: 0.6rem; color: var(--muted2); flex-shrink: 0 }
  .named-title   { font-size: 0.8rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .named-name:hover .named-title { color: var(--accent) }
  .named-count {
    font-size: 0.6rem; color: var(--muted); background: var(--sf2);
    border-radius: 10px; padding: 0 6px; flex-shrink: 0
  }
  .named-actions { display: flex; gap: 1px; flex-shrink: 0 }
  .named-actions button {
    background: none; border: none; color: var(--muted2); cursor: pointer;
    font-size: 0.72rem; width: 22px; height: 22px; border-radius: 5px;
    transition: background .12s, color .12s
  }
  .named-actions button:hover:not(:disabled) { background: var(--sf3); color: var(--accent) }
  .named-actions button:disabled { opacity: 0.25; cursor: not-allowed }
  .named-del:hover:not(:disabled) { color: #f08080 !important }
  .named-tracks { list-style: none; padding: 0 0 0.3rem }
  .named-track {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.3rem 0.6rem 0.3rem 1.6rem; cursor: pointer;
    transition: background .1s
  }
  .named-track:hover { background: var(--sf2) }
  .named-track-title { font-size: 0.74rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .named-track-rm { font-size: 0.58rem; color: transparent; cursor: pointer; margin-left: 0.4rem; flex-shrink: 0 }
  .named-track:hover .named-track-rm { color: var(--muted2) }
  .named-track-rm:hover { color: #f08080 !important }

  /* songs */
  .search-wrap { padding: 0.5rem 0.6rem; border-bottom: 1px solid var(--border) }

  .song-list { list-style: none }
  .song-item {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.45rem 0.7rem; cursor: pointer; border-left: 2px solid transparent;
    transition: background .1s
  }
  .song-item:hover { background: var(--sf2); border-left-color: var(--accent) }
  .song-main  { flex: 1; min-width: 0 }
  .song-title { display: block; font-size: 0.78rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .song-sub   { display: block; font-size: 0.66rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 1px }
  .song-play-icon { font-size: 0.6rem; color: transparent; margin-left: 0.4rem; transition: color .1s; flex-shrink: 0 }
  .song-item:hover .song-play-icon { color: var(--accent) }

  /* albums */
  .filter-bar {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.45rem 0.6rem; background: var(--accent-08);
    border-bottom: 1px solid var(--border)
  }
  .filter-lbl   { flex: 1; font-size: 0.72rem; color: var(--accent); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap }
  .filter-clear { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 0.7rem; padding: 2px 4px }
  .filter-clear:hover { color: #f08080 }

  .album-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 5px; padding: 6px }
  .album-card {
    background: var(--sf2); border-radius: var(--r); overflow: hidden;
    cursor: pointer; transition: transform .18s, box-shadow .18s;
    border: 1px solid var(--border)
  }
  .album-card:hover {
    transform: translateY(-3px) scale(1.02);
    box-shadow: 0 8px 24px #00000070, 0 0 0 1px var(--card-accent, var(--accent))44
  }
  .album-art, .album-placeholder {
    width: 100%; aspect-ratio: 1; display: block; object-fit: cover
  }
  .album-placeholder {
    display: flex; align-items: center; justify-content: center;
    background: var(--sf3); color: var(--muted2); font-size: 1.6rem
  }
  .album-meta   { padding: 0.3rem 0.4rem 0.4rem }
  .album-title  { font-size: 0.68rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .album-artist { font-size: 0.62rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 1px }

  /* artists */
  .artist-list { list-style: none }
  .artist-item {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.52rem 0.75rem; cursor: pointer; border-left: 2px solid transparent;
    transition: background .1s; border-bottom: 1px solid var(--border)
  }
  .artist-item:hover       { background: var(--sf2) }
  .artist-item.artist-active { background: var(--accent-08); border-left-color: var(--accent) }
  .artist-name  { font-size: 0.8rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .artist-arrow { font-size: 1rem; color: var(--muted2) }
  .artist-item:hover .artist-arrow { color: var(--accent) }

  /* scan progress */
  .scan-bar-wrap {
    padding: 0.4rem 0.6rem; border-top: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 0.25rem; flex-shrink: 0
  }
  .scan-label { font-size: 0.66rem; color: var(--muted) }
  .scan-track { height: 2px; background: var(--border); border-radius: 1px; overflow: hidden }
  .scan-fill  { height: 100%; background: var(--accent); border-radius: 1px; transition: width .3s }

  /* footer */
  .sidebar-footer {
    flex-shrink: 0; display: flex; gap: 3px;
    padding: 5px 6px; border-top: 1px solid var(--border)
  }
  .foot-btn {
    flex: 1; background: var(--sf2); border: 1px solid var(--border);
    color: var(--muted); font-size: 0.66rem; font-weight: 600;
    padding: 0.38rem 0; border-radius: 6px; cursor: pointer;
    transition: background .12s, color .12s
  }
  .foot-btn:hover        { background: var(--sf3); color: var(--text) }
  .foot-btn.accent       { color: var(--accent); border-color: var(--accent-30) }
  .foot-btn.accent:hover { background: var(--accent-15) }
  .foot-btn.danger       { color: #f08080; border-color: #7a202045 }
  .foot-btn.danger:hover { background: #3a1010 }
</style>
