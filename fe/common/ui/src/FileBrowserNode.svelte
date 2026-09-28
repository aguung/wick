<script lang="ts">
  import FileBrowserNode from "./FileBrowserNode.svelte";
  import type { SessionFileEntry } from "./file-browser-types.js";
  import type { FileTreeNode as TreeNode } from "./file-browser-tree.js";
  import { formatSize, formatRelTime } from "./file-meta.js";
  import KebabMenu from "./KebabMenu.svelte";

  type Props = {
    node: TreeNode;
    depth: number;
    forceOpen: boolean;
    openDirs: Record<string, boolean>;
    /** Folders whose children have been fetched. Distinguishes an empty
        folder from one nobody has opened yet. */
    loadedDirs?: Record<string, boolean>;
    loadingDirs?: Record<string, boolean>;
    deletingPaths?: Record<string, boolean>;
    onToggleDir: (path: string) => void;
    onOpen: (f: SessionFileEntry) => void;
    onDownload: (path: string) => void;
    onDelete: (path: string) => void;
    onNewHere: (dirPath: string) => void;
    /** Create a FOLDER inside this folder. Optional so a caller that only
        offers files keeps the menu honest rather than showing a dead row. */
    onNewDirHere?: (dirPath: string) => void;
    /** Re-root the tree at this folder. Only the Source panel has somewhere
        to go — the session rail IS the cwd — so the button appears only
        when a caller passes this. */
    onOpenDir?: (path: string) => void;
  };

  // The three maps are view hints, not data the row needs to exist: a caller
  // that has no lazy loading and no delete in flight should not have to say
  // so three times.
  let { node, depth, forceOpen, openDirs, loadedDirs = {}, loadingDirs = {}, deletingPaths = {}, onToggleDir, onOpen, onDownload, onDelete, onNewHere, onNewDirHere, onOpenDir }: Props = $props();

  const e = $derived(node.entry);
  const indent = $derived(depth * 14 + 8);
  const open = $derived(forceOpen || !!openDirs[e.path]);
  const busy = $derived(!!loadingDirs[e.path]);
  // A deleted row collapses in place so the rows below glide up, instead of
  // the list snapping and losing the reader's position.
  const deleting = $derived(!!deletingPaths[e.path]);
  const rowAnim = "overflow-hidden transition-all duration-150 ease-out";

  /* One ⋮ per row instead of a strip of icon buttons. Three reasons, in the
     order they bit us: the icons only appeared on hover, so on a touch screen
     they did not exist; they were absolutely positioned over a row whose whole
     left side is the open/close button, so a click that missed an icon by a
     pixel opened the folder instead; and every new action made the strip
     wider, pushing the file name further into truncation. */
  const folderItems = $derived([
    { label: "New file here", onclick: () => onNewHere(e.path) },
    ...(onNewDirHere ? [{ label: "New folder here", onclick: () => onNewDirHere(e.path) }] : []),
    ...(onOpenDir ? [{ label: "Open as root", onclick: () => onOpenDir(e.path) }] : []),
    { label: "Delete folder", onclick: () => onDelete(e.path), danger: true },
  ]);
  const fileItems = $derived([
    { label: "Download", onclick: () => onDownload(e.path) },
    { label: "Delete file", onclick: () => onDelete(e.path), danger: true },
  ]);
  const rowGone = "max-h-0 !py-0 opacity-0 border-b-0 pointer-events-none";
  const loaded = $derived(!!loadedDirs[e.path]);

  // Counted from what is actually loaded, so it appears the moment a folder
  // is opened and never pretends to know the size of one that is not.
  const dirCount = $derived(node.children.filter((c) => c.entry.isDir).length);
  const fileCount = $derived(node.children.length - dirCount);
  const countLabel = $derived(
    !loaded
      ? ""
      : dirCount === 0 && fileCount === 0
        ? "empty"
        : [dirCount ? `${dirCount} folder${dirCount === 1 ? "" : "s"}` : "",
           fileCount ? `${fileCount} file${fileCount === 1 ? "" : "s"}` : ""]
            .filter(Boolean)
            .join(" \u00b7 "),
  );
</script>

{#if e.isDir}
  <div class="group relative flex items-center gap-1.5 py-1.5 border-b border-white-300 dark:border-navy-600 max-h-16 {rowAnim} {deleting ? rowGone : ''}" style="padding-left:{indent}px;padding-right:8px;">
    <button type="button" onclick={() => onToggleDir(e.path)}
      class="flex items-center gap-1.5 min-w-0 flex-1 text-left">
      <span class="shrink-0 text-black-700 dark:text-black-600">
        <svg viewBox="0 0 16 16" class={`h-3 w-3 transition-transform ${open ? "rotate-90" : ""}`} fill="none" stroke="currentColor" stroke-width="1.5">
          <path d="M6 4l4 4-4 4" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
      </span>
      <span class="shrink-0">
        <svg viewBox="0 0 16 16" class="h-3.5 w-3.5 text-green-500" fill="none" stroke="currentColor" stroke-width="1.5">
          <path d="M2 4a1 1 0 011-1h3l2 2h5a1 1 0 011 1v6a1 1 0 01-1 1H3a1 1 0 01-1-1V4z" stroke-linejoin="round"/>
        </svg>
      </span>
      <span class="text-xs font-medium text-black-900 dark:text-white-100 truncate">{e.name}</span>
    </button>
    {#if busy}
      <span class="shrink-0 text-[10px] text-black-700 dark:text-black-600 transition-opacity group-hover:opacity-0">loading…</span>
    {:else if open && countLabel}
      <span class="shrink-0 text-[10px] text-black-700 dark:text-black-600 font-mono transition-opacity group-hover:opacity-0">{countLabel}</span>
    {/if}
    <div class="shrink-0 opacity-60 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
      <KebabMenu items={folderItems} size="sm" width={184} ariaLabel={`Actions for folder ${e.name}`} />
    </div>
  </div>
  {#if open && !deleting}
    {#each node.children as child (child.entry.path)}
      <FileBrowserNode node={child} depth={depth + 1} {forceOpen} {openDirs} {loadedDirs} {loadingDirs} {deletingPaths} {onToggleDir} {onOpen} {onDownload} {onDelete} {onNewHere} {onNewDirHere} {onOpenDir} />
    {/each}
  {/if}
{:else}
  <div class="group relative flex items-center gap-1.5 py-1.5 border-b border-white-300 dark:border-navy-600 max-h-16 {rowAnim} {deleting ? rowGone : ''}" style="padding-left:{indent + 18}px;padding-right:8px;">
    <span class="shrink-0 text-black-700 dark:text-black-600">
      <svg viewBox="0 0 16 16" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.5">
        <path d="M3 2h6l3 3v9a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z M9 2v3h3" stroke-linejoin="round"/>
      </svg>
    </span>
    <button type="button" onclick={() => onOpen(e)} class="min-w-0 flex-1 text-left pr-2">
      <div class="text-xs text-black-900 dark:text-white-100 truncate">{e.name}</div>
      <div class="text-[10px] text-black-700 dark:text-black-600 truncate font-mono">{formatSize(e.size)} · {formatRelTime(e.mtime)}</div>
    </button>
    <div class="shrink-0 opacity-60 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
      <KebabMenu items={fileItems} size="sm" width={168} ariaLabel={`Actions for ${e.name}`} />
    </div>
  </div>
{/if}
