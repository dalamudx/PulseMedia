export type SiteProbeTarget = { id: string; configVersion: number; name: string };
export type SiteProbeReply = { status: number; alive?: boolean; latencyMs?: number | null };
export type SiteProbeEntry = {
  target: SiteProbeTarget;
  state: 'queued' | 'running' | 'online' | 'offline' | 'unknown' | 'stale' | 'cancelled' | 'rejected';
  latencyMs: number | null;
  observedAt: number | null;
  status?: number;
};
export type SiteProbeBatchSnapshot = {
  running: boolean;
  stopping: boolean;
  entries: readonly SiteProbeEntry[];
};

export function createSiteProbeBatch(deps: {
  getSites: () => readonly SiteProbeTarget[];
  probe: (target: SiteProbeTarget, signal: AbortSignal) => Promise<SiteProbeReply>;
  refresh: () => Promise<void>;
  canRun: () => boolean;
  onChange: (state: SiteProbeBatchSnapshot) => void;
  now: () => number;
}): {
  start: (ids: readonly string[]) => Promise<void>;
  stopQueued: () => void;
  dispose: () => void;
} {
  let entries: SiteProbeEntry[] = [];
  let running = false;
  let stopping = false;
  let disposed = false;
  let controller: AbortController | undefined;
  const clear = (entry: SiteProbeEntry, state: SiteProbeEntry['state']) => {
    entry.state = state;
    entry.latencyMs = null;
    entry.observedAt = null;
  };
  const current = (target: SiteProbeTarget) => deps.getSites().some(site =>
    site.id === target.id && site.configVersion === target.configVersion);
  const cancelQueued = () => {
    for (const entry of entries) if (entry.state === 'queued') clear(entry, 'cancelled');
  };
  const emit = () => {
    if (disposed) return;
    const allowed = deps.canRun();
    if (!allowed) {
      stopping = running;
      cancelQueued();
    }
    for (const entry of entries) {
      if (['online', 'offline', 'unknown'].includes(entry.state)) {
        if (!allowed) clear(entry, 'unknown');
        else if (!current(entry.target)) clear(entry, 'stale');
      }
    }
    deps.onChange({ running, stopping, entries: entries.map(entry => ({ ...entry, target: { ...entry.target } })) });
  };
  const stopQueued = () => {
    if (disposed || !running) return;
    stopping = true;
    cancelQueued();
    emit();
  };
  const start = async (ids: readonly string[]) => {
    if (disposed || running || !deps.canRun()) return;
    const sites = deps.getSites();
    entries = [...new Set(ids)].flatMap(id => {
      const target = sites.find(site => site.id === id);
      return target ? [{ target: { ...target }, state: 'queued' as const, latencyMs: null, observedAt: null }] : [];
    });
    running = true;
    stopping = false;
    try {
      emit();
      for (const entry of entries) {
        if (disposed) break;
        if (!deps.canRun()) stopQueued();
        if (stopping || disposed) break;
        if (!current(entry.target)) {
          clear(entry, 'stale');
          emit();
          continue;
        }
        entry.state = 'running';
        emit();
        // Callbacks may stop the batch or change the list before transport starts.
        if (disposed) break;
        if (!deps.canRun() || stopping) {
          clear(entry, 'cancelled');
          stopQueued();
          break;
        }
        if (!current(entry.target)) {
          clear(entry, 'stale');
          emit();
          continue;
        }
        controller = new AbortController();
        let halt = false;
        try {
          const reply = await deps.probe({ ...entry.target }, controller.signal);
          if (disposed) break;
          if (!reply || !Number.isInteger(reply.status) || reply.status < 100 || reply.status > 599) {
            clear(entry, 'unknown');
            halt = true;
          } else {
            entry.status = reply.status;
            if (reply.status >= 200 && reply.status < 300) {
              const observedAt = deps.now();
              if (typeof reply.alive !== 'boolean' || !Number.isFinite(observedAt) || observedAt < 0) {
                clear(entry, 'unknown');
                halt = true;
              } else {
                entry.state = reply.alive ? 'online' : 'offline';
                entry.observedAt = observedAt;
                // Bad latency does not invalidate an explicit boolean health result.
                entry.latencyMs = Number.isSafeInteger(reply.latencyMs) && reply.latencyMs! >= 0 ? reply.latencyMs! : null;
              }
            } else {
              clear(entry, 'rejected');
              halt = reply.status === 401 || reply.status === 403;
            }
          }
        } catch {
          if (disposed) break;
          clear(entry, 'unknown');
          halt = true;
        }
        if (halt) stopQueued();
        emit();
        if (disposed) break;
        if (deps.canRun()) {
          try {
            await deps.refresh();
          } catch {
            if (disposed) break;
            if (entry.status !== 401 && entry.status !== 403) clear(entry, 'unknown');
            stopQueued();
          }
        } else stopQueued();
        if (disposed) break;
        // Refresh may reveal replacement/deletion, including earlier successes.
        emit();
      }
    } finally {
      controller = undefined;
      running = false;
      stopping = false;
      emit();
    }
  };
  return {
    start,
    stopQueued,
    dispose: () => {
      disposed = true;
      cancelQueued();
      // Aborting the context makes no claim about physical server completion.
      controller?.abort();
    },
  };
}
