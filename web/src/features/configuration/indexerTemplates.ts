import type { paths } from "@/api/client";

export type IndexerTemplate = paths["/api/v1/indexer-templates"]["get"]["responses"]["200"]["content"]["application/json"]["templates"][number];

export interface SiteDraftLike {
  name: string;
  protocol: "torznab" | "nexusphp" | "rss";
  url: string;
  categories: string;
  keepURL?: boolean;
}

/**
 * Apply a selected template to an indexer draft.
 * If editing and keepURL is true, existing URL is preserved.
 * Otherwise, default_url from template is used.
 */
export function applyTemplateToDraft<T extends SiteDraftLike>(
  draft: T,
  template: IndexerTemplate,
  isEditing: boolean = false
): T {
  const shouldKeepURL = isEditing && !!draft.keepURL;
  return {
    ...draft,
    name: template.name,
    protocol: template.protocol,
    url: shouldKeepURL ? draft.url : template.default_url,
    categories: template.recommended_categories.join(", "),
  };
}

/**
 * Groups templates into display categories:
 * - torznab (aggregators & global)
 * - nexusphp (domestic mainstream)
 * - rss (public feeds)
 */
export function groupTemplatesByProtocol(templates: IndexerTemplate[]) {
  return {
    torznab: templates.filter(t => t.protocol === "torznab"),
    nexusphp: templates.filter(t => t.protocol === "nexusphp"),
    rss: templates.filter(t => t.protocol === "rss"),
  };
}
