<script lang="ts">
  // Browse and edit the files of the SELECTED repo without leaving the
  // Source panel. The tree is rooted at $activeRepo and re-roots whenever
  // that selection moves — the whole point of having it here rather than in
  // the session Files panel, which always shows the session cwd.
  //
  // Listings are lazy, one request per folder: a session can hold dozens of
  // clones, and preloading the tree is both slow and, past the server's cap,
  // silently incomplete.
  import { untrack } from "svelte";
  import { get } from "svelte/store";
  import * as files from "$lib/api/files";
  import type { FileEntry, FileContent } from "$lib/api/files";
  import { sessionID, activeRepo, loadStatus } from "$lib/stores/scm";
  import { langFor } from "$lib/git-actions";
  import { toastOk, toastError } from "@wick-fe/common-stores";
  import { ConfirmDialog } from "@wick-fe/common-ui";
  import MonacoView from "$lib/components/MonacoView.svelte";
  import {
    sessionPath, toRepoRel, joinRel, parentRel, ancestorRels, breadcrumbs,
    baseName, isWithin, sortEntries, normalizeRel,
  } from "$lib/files-root";

  type Props = {
    // sidebar: the tree fills the dock and a file opens in an overlay.
    // full: tree on the left, editor beside it.
    mode?: "sidebar" | "full";
  };
  let { mode = "full" }: Props = $props();

  // Everything below is REPO-relative; files-root converts on the way to and
  // from the API. "" is the repo root.
  let dirs = $state<Record<string, FileEntry[]>>({});
  let loadingDirs = $state<Record<string, boolean>>({});
  let truncatedDirs = $state<Record<string, boolean>>({});
  let expanded = $state<Record<string, boolean>>({});
  // The folder the tree hangs from — moved by the breadcrumb and by the
  // "open as root" arrow on a folder row. Deep trees get unreadable in a
  // 300px dock, so scoping down is worth a click.
  let root = $state("");
  let filter = $state("");

  let openPath = $state<string | null>(null);
  let content = $state<FileContent | null>(null);
  // Non-null means the editor holds an unsaved edit.
  let buffer = $state<string | null>(null);
  let saving = $state(false);

  let creating = $state<{ isDir: boolean } | null>(null);
  let newName = $state("");
  let deleteAsk = $state<{ path: string; isDir: boolean } | null>(null);

  const crumbs = $derived(breadcrumbs($activeRepo, root));
  const editable = $derived(!!content && !content.binary && !content.tooBig);
  const lang = $derived(openPath ? langFor(openPath) : "plaintext");
  const dirty = $derived(buffer !== null);
  const downloadHref = $derived(
    openPath ? files.downloadURL($sessionID, sessionPath($activeRepo, openPath)) : "",
  );

  // Flattened rows, so indentation and the filter are one pass instead of a
  // recursive component. Only expanded folders contribute children, and a
  // folder contributes nothing until its listing has arrived.
  type Row = { entry: FileEntry; depth: number };
  const rows = $derived.by(() => {
    const out: Row[] = [];
    const walk = (dir: string, depth: number) => {
      for (const e of dirs[dir] ?? []) {
        out.push({ entry: e, depth });
        if (e.isDir && expanded[e.path]) walk(e.path, depth + 1);
      }
    };
    walk(root, 0);
    return out;
  });
  const needle = $derived(filter.trim().toLowerCase());
  // The filter narrows what is ALREADY loaded — it is a way to find a name
  // in a big folder, not a tree-wide search (that would mean walking every
  // clone in the session on each keystroke).
  const visible = $derived(
    needle ? rows.filter((r) => r.entry.name.toLowerCase().includes(needle)) : rows,
  );

  async function loadDir(dir: string, force = false): Promise<void> {
    if (!force && dirs[dir]) return;
    const id = get(sessionID);
    if (!id) return;
    const repo = get(activeRepo);
    loadingDirs = { ...loadingDirs, [dir]: true };
    try {
      const r = await files.listDir(id, sessionPath(repo, dir));
      // A listing that lands after the user switched repos describes a tree
      // that no longer exists; toRepoRel rejects its paths rather than
      // hanging one repo's files under another.
      if (get(activeRepo) !== repo) return;
      const entries: FileEntry[] = [];
      for (const f of r.files) {
        const rel = toRepoRel(repo, f.path);
        if (!rel) continue;
        entries.push({ ...f, path: rel });
      }
      dirs = { ...dirs, [dir]: sortEntries(entries) };
      truncatedDirs = { ...truncatedDirs, [dir]: r.truncated === true };
    } catch (e) {
      toastError("Files", String(e));
    } finally {
      loadingDirs = { ...loadingDirs, [dir]: false };
    }
  }

  function toggleDir(path: string) {
    const next = !expanded[path];
    expanded = { ...expanded, [path]: next };
    if (next) void loadDir(path);
  }

  function setRoot(path: string) {
    root = path;
    filter = "";
    void loadDir(path);
  }

  // Leaving a dirty buffer behind silently is the one way this panel could
  // lose work, so it asks — the same shape deleteBranch uses for git's
  // "not fully merged" refusal.
  function mayLeave(): boolean {
    if (buffer === null || !openPath) return true;
    return confirm(`Discard unsaved changes to ${baseName(openPath)}?`);
  }

  async function openFile(path: string) {
    if (openPath === path) return;
    if (!mayLeave()) return;
    openPath = path;
    content = null;
    buffer = null;
    const repo = get(activeRepo);
    try {
      const c = await files.readFile(get(sessionID), sessionPath(repo, path));
      if (get(activeRepo) !== repo || openPath !== path) return;
      content = c;
    } catch (e) {
      toastError("Open failed", String(e));
      if (openPath === path) openPath = null;
    }
  }

  function closeFile() {
    if (!mayLeave()) return;
    openPath = null;
    content = null;
    buffer = null;
  }

  async function save() {
    const path = openPath;
    const text = buffer;
    if (path === null || text === null) return;
    saving = true;
    const repo = get(activeRepo);
    try {
      await files.saveFile(get(sessionID), sessionPath(repo, path), text);
      if (openPath === path && get(activeRepo) === repo) {
        content = content ? { ...content, content: text } : content;
        buffer = null;
      }
      toastOk("Saved", path);
      // The file is very likely tracked: refresh the snapshot so the Changes
      // tab and the rail badge count the edit that just happened.
      void loadStatus();
    } catch (e) {
      toastError("Save failed", String(e));
    } finally {
      saving = false;
    }
  }

  async function submitCreate() {
    const c = creating;
    if (!c) return;
    // A name may carry slashes — "lib/util.ts" creates the folder too, which
    // is quicker than making each level by hand.
    const name = normalizeRel(newName);
    if (!name) return;
    const path = joinRel(root, name);
    const repo = get(activeRepo);
    try {
      await files.createEntry(get(sessionID), sessionPath(repo, path), c.isDir);
      creating = null;
      newName = "";
      toastOk(c.isDir ? "Folder created" : "File created", path);
      // Re-list every folder on the way down so the new entry is visible
      // even when the name created intermediate levels.
      for (const a of ancestorRels(path)) {
        if (a) expanded = { ...expanded, [a]: true };
        await loadDir(a, true);
      }
      if (c.isDir) {
        expanded = { ...expanded, [path]: true };
        void loadDir(path, true);
      } else {
        await openFile(path);
      }
      void loadStatus();
    } catch (e) {
      toastError("Create failed", String(e));
    }
  }

  async function confirmDelete() {
    const d = deleteAsk;
    deleteAsk = null;
    if (!d) return;
    const repo = get(activeRepo);
    try {
      await files.deleteEntry(get(sessionID), sessionPath(repo, d.path));
      toastOk("Deleted", d.path);
      if (openPath && isWithin(d.path, openPath)) {
        openPath = null;
        content = null;
        buffer = null;
      }
      // Drop every cached listing that lived inside it — those folders are
      // gone, and keeping them would draw rows for files that no longer are.
      const keep: Record<string, FileEntry[]> = {};
      for (const [k, v] of Object.entries(dirs)) {
        if (!isWithin(d.path, k)) keep[k] = v;
      }
      dirs = keep;
      if (isWithin(d.path, root)) root = parentRel(d.path);
      await loadDir(parentRel(d.path), true);
      void loadStatus();
    } catch (e) {
      toastError("Delete failed", String(e));
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) {
      if (buffer === null) return;
      e.preventDefault();
      void save();
      return;
    }
    if (e.key === "Escape" && mode === "sidebar" && openPath) closeFile();
  }

  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  }

  // Re-root on every repo switch (and once the session id arrives — the
  // panel mounts before it is told which session it belongs to).
  $effect(() => {
    void $activeRepo;
    void $sessionID;
    untrack(() => {
      dirs = {};
      loadingDirs = {};
      truncatedDirs = {};
      expanded = {};
      root = "";
      filter = "";
      openPath = null;
      content = null;
      buffer = null;
      creating = null;
      newName = "";
      void loadDir("", true);
    });
  });
</script>

<svelte:window onkeydown={onKeydown} />

{#snippet tree()}
  <!-- Breadcrumb: where the tree is rooted, and the way back up. At the repo
       root there is nothing to say — the crumb would just repeat the repo
       name the header above already carries — so the row only appears once
       you are inside a folder, and the root itself is a home button rather
       than the repo's name spelled a second time. -->
  {#if crumbs.length > 1}
    <div class="flex items-center gap-0.5 overflow-x-auto border-b border-white-300 dark:border-navy-600 px-2 py-1 text-[11px]">
      {#each crumbs as c, i (c.path)}
        {#if i > 0}<span class="shrink-0 text-black-600">/</span>{/if}
        {#if i === 0}
          <button
            type="button"
            onclick={() => setRoot("")}
            title="Repository root"
            aria-label="Repository root"
            class="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-black-700 transition-colors hover:bg-white-200 dark:text-black-600 dark:hover:bg-navy-800"
          >
            <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M2 7l6-4.5L14 7M3.5 6v7h9V6" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
        {:else}
          <button
            type="button"
            onclick={() => setRoot(c.path)}
            class={"shrink-0 truncate rounded px-1 py-0.5 transition-colors hover:bg-white-200 dark:hover:bg-navy-800 " + (i === crumbs.length - 1 ? "font-medium text-black-900 dark:text-white-100" : "text-black-700 dark:text-black-600")}
          >{c.name}</button>
        {/if}
      {/each}
    </div>
  {/if}

  <!-- Filter + create + refresh -->
  <div class="flex items-center gap-1 border-b border-white-300 dark:border-navy-600 px-2 py-1">
    <input
      type="text"
      bind:value={filter}
      placeholder="Filter…"
      class="min-w-0 flex-1 rounded border border-white-300 dark:border-navy-600 bg-white-100 dark:bg-navy-800 px-1.5 py-0.5 text-[11px] text-black-900 dark:text-white-100 placeholder:text-black-600 focus:border-green-500 focus:outline-none"
    />
    <button
      type="button"
      title="New file"
      onclick={() => { creating = { isDir: false }; newName = ""; }}
      class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800"
    >
      <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M9 2H4.5A1.5 1.5 0 003 3.5v9A1.5 1.5 0 004.5 14h7a1.5 1.5 0 001.5-1.5V6L9 2z" stroke-linejoin="round"/><path d="M9 2v4h4M8 8v4M6 10h4" stroke-linecap="round"/></svg>
    </button>
    <button
      type="button"
      title="New folder"
      onclick={() => { creating = { isDir: true }; newName = ""; }}
      class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800"
    >
      <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M2 5.5A1.5 1.5 0 013.5 4h2.2l1.2 1.5h5.6A1.5 1.5 0 0114 7v4.5a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 11.5v-6z" stroke-linejoin="round"/><path d="M8 7.5v4M6 9.5h4" stroke-linecap="round"/></svg>
    </button>
    <button
      type="button"
      title="Refresh"
      onclick={() => loadDir(root, true)}
      class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800"
    >
      <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M2 8a6 6 0 0110.5-4M14 8a6 6 0 01-10.5 4M11 2v3h3M5 14v-3H2" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
  </div>

  {#if creating}
    <!-- svelte-ignore a11y_autofocus -->
    <form
      class="flex items-center gap-1 border-b border-white-300 dark:border-navy-600 bg-white-200 dark:bg-navy-800 px-2 py-1"
      onsubmit={(e) => { e.preventDefault(); void submitCreate(); }}
    >
      <span class="shrink-0 text-[10px] text-black-600">{creating.isDir ? "Folder" : "File"} in {crumbs.length > 1 ? crumbs[crumbs.length - 1].name + "/" : "repo root"}</span>
      <input
        type="text"
        autofocus
        bind:value={newName}
        placeholder={creating.isDir ? "name" : "name.ts"}
        onkeydown={(e) => { if (e.key === "Escape") { creating = null; newName = ""; } }}
        class="min-w-0 flex-1 rounded border border-white-300 dark:border-navy-600 bg-white-100 dark:bg-navy-700 px-1.5 py-0.5 font-mono text-[11px] text-black-900 dark:text-white-100 focus:border-green-500 focus:outline-none"
      />
      <button type="submit" class="shrink-0 rounded bg-green-500 px-2 py-0.5 text-[11px] font-medium text-white-100 hover:bg-green-600">Create</button>
      <button type="button" onclick={() => { creating = null; newName = ""; }} class="shrink-0 rounded border border-white-300 dark:border-navy-600 px-2 py-0.5 text-[11px] text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-700">Cancel</button>
    </form>
  {/if}

  <!-- Rows -->
  <div class="flex-1 overflow-y-auto py-0.5">
    {#if loadingDirs[root] && !dirs[root]}
      <p class="px-3 py-3 text-[11px] text-black-700 dark:text-black-600">Loading…</p>
    {:else if visible.length === 0}
      <p class="px-3 py-3 text-[11px] text-black-700 dark:text-black-600">
        {needle ? "Nothing matches the filter." : "This folder is empty."}
      </p>
    {/if}
    {#each visible as row (row.entry.path)}
      {@const e = row.entry}
      {@const isOpen = openPath === e.path}
      <div
        class={"group flex items-center gap-1 pr-1 transition-colors " + (isOpen ? "bg-white-300 dark:bg-navy-600" : "hover:bg-white-200 dark:hover:bg-navy-800")}
        style={`padding-left:${(needle ? 0 : row.depth) * 10 + 4}px`}
      >
        <button
          type="button"
          onclick={() => (e.isDir ? toggleDir(e.path) : openFile(e.path))}
          title={e.path}
          class="flex min-w-0 flex-1 items-center gap-1 py-1 text-left"
        >
          {#if e.isDir}
            <svg viewBox="0 0 16 16" class={"h-3 w-3 shrink-0 text-black-600 transition-transform " + (expanded[e.path] ? "rotate-90" : "")} fill="none" stroke="currentColor" stroke-width="1.6"><path d="M6 4l4 4-4 4" stroke-linecap="round" stroke-linejoin="round"/></svg>
            <span class="min-w-0 flex-1 truncate text-[11px] font-medium text-black-900 dark:text-white-100">{e.name}</span>
          {:else}
            <span class="h-3 w-3 shrink-0"></span>
            <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-black-900 dark:text-white-100">{e.name}</span>
          {/if}
          {#if needle && parentRel(e.path)}
            <span class="shrink-0 truncate font-mono text-[9px] text-black-600">{parentRel(e.path)}</span>
          {/if}
        </button>
        {#if e.isDir}
          <button
            type="button"
            title="Open this folder as the root"
            onclick={() => setRoot(e.path)}
            class="hidden h-5 w-5 shrink-0 items-center justify-center rounded text-black-600 hover:bg-white-300 dark:hover:bg-navy-600 group-hover:inline-flex"
          >
            <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 8h9M9 5l3 3-3 3" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
        {:else}
          <a
            href={files.downloadURL($sessionID, sessionPath($activeRepo, e.path))}
            title="Download"
            download
            class="hidden h-5 w-5 shrink-0 items-center justify-center rounded text-black-600 hover:bg-white-300 dark:hover:bg-navy-600 group-hover:inline-flex"
          >
            <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M8 2v8M5 7l3 3 3-3M3 13h10" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </a>
        {/if}
        <button
          type="button"
          title="Delete"
          onclick={() => (deleteAsk = { path: e.path, isDir: e.isDir })}
          class="hidden h-5 w-5 shrink-0 items-center justify-center rounded text-black-600 hover:bg-white-300 hover:text-red-600 dark:hover:bg-navy-600 dark:hover:text-red-400 group-hover:inline-flex"
        >
          <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 4h10M6.5 4V2.5h3V4M5 4l.5 9h5L11 4" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      </div>
    {/each}
    {#if truncatedDirs[root]}
      <p class="px-3 py-2 text-[10px] text-amber-600 dark:text-amber-400">This folder has more entries than the panel lists.</p>
    {/if}
  </div>
{/snippet}

{#snippet editor()}
  <div class="flex h-full min-h-0 flex-col">
    <div class="flex items-center gap-2 border-b border-white-300 dark:border-navy-600 bg-white-100 dark:bg-navy-700 px-3 py-1.5">
      <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-black-900 dark:text-white-100">{openPath}</span>
      {#if dirty}
        <button type="button" onclick={save} disabled={saving} class="shrink-0 rounded bg-green-500 px-2 py-0.5 text-[11px] font-medium text-white-100 hover:bg-green-600 disabled:opacity-50">Save</button>
        <button type="button" onclick={() => (buffer = null)} class="shrink-0 rounded border border-white-300 dark:border-navy-600 px-2 py-0.5 text-[11px] text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800">Revert edit</button>
      {/if}
      <a
        href={downloadHref}
        download
        title="Download"
        class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800"
      >
        <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M8 2v8M5 7l3 3 3-3M3 13h10" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </a>
      <button type="button" onclick={closeFile} title="Close" class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800">
        <svg viewBox="0 0 16 16" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 4l8 8M12 4l-8 8" stroke-linecap="round"/></svg>
      </button>
    </div>
    <div class="min-h-0 flex-1">
      {#if !content}
        <div class="flex h-full items-center justify-center text-xs text-black-700 dark:text-black-600">Loading…</div>
      {:else if !editable}
        <!-- Bytes Monaco cannot show: a binary file, or one past the
             server's 2 MiB read cap. Say which, and offer the download. -->
        <div class="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
          <p class="text-xs text-black-700 dark:text-black-600">
            {content.binary ? "This looks like a binary file" : "This file is too large to edit here"}
            — {fmtSize(content.size)}.
          </p>
          <a href={downloadHref} download class="rounded-lg border border-white-300 dark:border-navy-600 px-2.5 py-1 text-[11px] text-black-700 dark:text-black-600 hover:bg-white-200 dark:hover:bg-navy-800">Download</a>
        </div>
      {:else}
        <!-- Keyed on the path so switching files builds a fresh model
             instead of streaming a new value through the live one, which
             would look like the user had just typed the whole file. -->
        {#key openPath}
          <MonacoView
            mode="edit"
            modified={buffer ?? content.content ?? ""}
            language={lang}
            onChange={(v) => (buffer = v === (content?.content ?? "") ? null : v)}
          />
        {/key}
      {/if}
    </div>
  </div>
{/snippet}

{#if mode === "sidebar"}
  <div class="flex min-h-0 flex-1 flex-col">
    {@render tree()}
  </div>
  {#if openPath}
    <!-- The dock is too narrow for an editor, so the file opens over the
         panel — the same shape the Changes tab's DiffModal uses. -->
    <div
      class="fixed inset-0 z-[60] flex items-end justify-center bg-black/60 backdrop-blur-sm sm:items-center sm:p-4"
      role="presentation"
      onclick={(e) => { if (e.target === e.currentTarget) closeFile(); }}
    >
      <div class="flex h-full w-full flex-col overflow-hidden border-t border-white-300 bg-white-100 shadow-2xl sm:h-[90vh] sm:max-w-6xl sm:rounded-2xl sm:border dark:border-navy-600 dark:bg-navy-700">
        {@render editor()}
      </div>
    </div>
  {/if}
{:else}
  <div class="flex min-h-0 w-full flex-1 overflow-hidden">
    <aside class="flex w-[260px] shrink-0 flex-col overflow-hidden border-r border-white-300 dark:border-navy-600 bg-white-100 dark:bg-navy-700">
      {@render tree()}
    </aside>
    <main class="flex min-w-0 flex-1 flex-col overflow-hidden bg-white-200 dark:bg-navy-800">
      {#if openPath}
        {@render editor()}
      {:else}
        <div class="flex h-full items-center justify-center text-xs text-black-700 dark:text-black-600">Select a file to edit.</div>
      {/if}
    </main>
  </div>
{/if}

<ConfirmDialog
  open={!!deleteAsk}
  title={deleteAsk?.isDir ? "Delete folder?" : "Delete file?"}
  body={deleteAsk
    ? `Delete ${deleteAsk.path}${deleteAsk.isDir ? " and everything inside it" : ""}? This removes it from disk and cannot be undone.`
    : ""}
  confirmLabel="Delete"
  cancelLabel="Cancel"
  destructive={true}
  onConfirm={confirmDelete}
  onCancel={() => (deleteAsk = null)}
/>
