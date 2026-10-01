/* "Last updated" metadata of a live model list (omp/opencode): when the
   server last read it, from where ("files" / "server" = read without
   starting the CLI, "cli" = an explicit Refresh), and whether the caller may
   Refresh it (runs the CLI once). Carried ON the model array a loader
   returns — a non-enumerable property — so every picker / composer loader
   keeps its `Promise<ComposerModelOption[]>` contract. */
export type ModelListMeta = {
  fetchedAt?: string;
  source?: string;
  canRefresh?: boolean;
};

const META = Symbol.for("wick.modelListMeta");

/** Attaches the server's stamp (fetched_at / source / can_refresh) to list. */
export function withModelListMeta<T>(
  list: T[],
  r: { fetched_at?: string; source?: string; can_refresh?: boolean } | null | undefined,
): T[] {
  if (r && (r.fetched_at || r.source || r.can_refresh)) {
    Object.defineProperty(list, META, {
      value: { fetchedAt: r.fetched_at || undefined, source: r.source || undefined, canRefresh: !!r.can_refresh } satisfies ModelListMeta,
      enumerable: false,
    });
  }
  return list;
}

/** The meta withModelListMeta attached, if any. */
export function modelListMeta(list: unknown): ModelListMeta | undefined {
  if (!list || typeof list !== "object") return undefined;
  return (list as Record<symbol, ModelListMeta | undefined>)[META];
}

/** "Updated 07:55 · files" / "No model list yet — click Refresh". */
export function describeModelListMeta(m: ModelListMeta | undefined): string {
  if (!m) return "";
  if (!m.fetchedAt) return m.canRefresh ? "No model list yet — click Refresh" : "No model list yet";
  const d = new Date(m.fetchedAt);
  const t = Number.isNaN(d.getTime()) ? m.fetchedAt : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return `Updated ${t}${m.source ? ` · ${m.source}` : ""}`;
}
