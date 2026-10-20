import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
import type { MediaItem } from "@/components/cards/MediaCard";
import {
  discoveryMediaItem,
  discoverySearchInput,
  doubanCategoryToList,
  doubanMediaItem,
  isDoubanCategory,
  type DiscoveryCategory,
  type DiscoverySearchType,
} from "./discoveryCatalog";

type Scope = {
  category: DiscoveryCategory;
  locationKey: string;
  userId: string | undefined;
  role: string | undefined;
  active: boolean;
  feed: AbortController | null;
  search: AbortController | null;
};
type Feed = { scope: Scope; items: MediaItem[]; busy: boolean; error: string };
type Search = Feed & { searched: boolean };

export function useDiscoveryCatalog({ category, locationKey, userId, role }: {
  category: DiscoveryCategory;
  locationKey: string;
  userId: string | undefined;
  role?: string;
}) {
  const scope = useMemo<Scope>(() => ({ category, locationKey, userId, role, active: false, feed: null, search: null }), [category, locationKey, userId, role]);
  const current = useRef(scope);
  current.current = scope;
  const [feed, setFeed] = useState<Feed>(() => ({ scope, items: [], busy: false, error: "" }));
  const [result, setResult] = useState<Search>(() => ({ scope, items: [], busy: false, error: "", searched: false }));
  const active = useCallback(() => current.current === scope && scope.active, [scope]);

  const refreshFeed = useCallback(async (): Promise<boolean> => {
    if (!active() || !scope.userId) return false;
    scope.feed?.abort();
    const controller = new AbortController();
    scope.feed = controller;
    const owns = () => active() && scope.feed === controller && !controller.signal.aborted;
    setFeed(previous => ({ scope, items: previous.scope === scope ? previous.items : [], busy: true, error: "" }));
    try {
      if (isDoubanCategory(scope.category)) {
        const listName = doubanCategoryToList[scope.category];
        const { data, error, response } = await api.GET("/api/v1/discover/douban", {
          params: { query: { list: listName, page: 1 } },
          signal: controller.signal,
        });
        if (!owns()) return false;
        if (error || !data || !Array.isArray(data.results)) {
          setFeed(previous => ({
            ...previous,
            items: response?.status === 401 || response?.status === 403 ? [] : previous.items,
            error: "豆瓣榜单读取失败，请检查豆瓣配置或服务状态。",
          }));
          return false;
        }
        setFeed({ scope, items: data.results.map(doubanMediaItem), busy: false, error: "" });
        return true;
      }

      const { data, error, response } = await api.GET("/api/v1/discover", {
        params: { query: { category: scope.category, page: 1 } },
        signal: controller.signal,
      });
      if (!owns()) return false;
      if (error || !data || !Array.isArray(data.results)) {
        setFeed(previous => ({ ...previous, items: response?.status === 401 || response?.status === 403 ? [] : previous.items, error: "推荐读取失败，请检查元数据配置或重新加载。" }));
        return false;
      }
      setFeed({ scope, items: data.results.map(discoveryMediaItem), busy: false, error: "" });
      return true;
    } catch {
      if (owns()) setFeed(previous => ({ ...previous, error: "连接失败，未取得推荐数据。" }));
      return false;
    } finally {
      if (owns()) {
        scope.feed = null;
        setFeed(previous => ({ ...previous, busy: false }));
      }
    }
  }, [scope, active]);

  const resetSearch = useCallback((): void => {
    if (!active()) return;
    scope.search?.abort();
    scope.search = null;
    setResult({ scope, items: [], busy: false, error: "", searched: false });
  }, [scope, active]);

  const search = useCallback(async (query: string, type: DiscoverySearchType, year: string): Promise<boolean> => {
    if (!active() || !scope.userId) return false;
    scope.search?.abort();
    scope.search = null;
    const input = discoverySearchInput(query, type, year);
    if ("error" in input) {
      setResult({ scope, items: [], busy: false, error: input.error, searched: true });
      return false;
    }
    const controller = new AbortController();
    scope.search = controller;
    const owns = () => active() && scope.search === controller && !controller.signal.aborted;
    setResult({ scope, items: [], busy: true, error: "", searched: true });
    try {
      const { data, error } = await api.GET("/api/v1/search/movies", {
        params: { query: input }, signal: controller.signal,
      });
      if (!owns()) return false;
      if (error || !data || !Array.isArray(data.results)) {
        setResult({ scope, items: [], busy: false, error: "搜索失败，请检查元数据配置或重试。", searched: true });
        return false;
      }
      setResult({ scope, items: data.results.map(discoveryMediaItem), busy: false, error: "", searched: true });
      return true;
    } catch {
      if (owns()) setResult({ scope, items: [], busy: false, error: "连接失败，未取得搜索结果。", searched: true });
      return false;
    } finally {
      if (owns()) {
        scope.search = null;
        setResult(previous => ({ ...previous, busy: false }));
      }
    }
  }, [scope, active]);

  useEffect(() => {
    scope.active = true;
    setFeed({ scope, items: [], busy: false, error: "" });
    setResult({ scope, items: [], busy: false, error: "", searched: false });
    void refreshFeed();
    return () => {
      scope.active = false;
      scope.feed?.abort();
      scope.search?.abort();
      scope.feed = null;
      scope.search = null;
    };
  }, [scope, refreshFeed]);

  // Mask old scopes during render, before passive-effect cleanup can run.
  const feedVisible = feed.scope === scope && !!userId;
  const searchVisible = result.scope === scope && !!userId;
  return {
    feedItems: feedVisible ? feed.items : [],
    movies: searchVisible ? result.items : [],
    loadingFeed: feedVisible && feed.busy,
    searching: searchVisible && result.busy,
    searched: searchVisible && result.searched,
    feedError: feedVisible ? feed.error : "",
    searchError: searchVisible ? result.error : "",
    search, refreshFeed, resetSearch,
  };
}
