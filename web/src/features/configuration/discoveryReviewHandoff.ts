export type DiscoveryReviewCandidateRef = Readonly<{
  userId: string;
  subscriptionId: string;
  candidateId: string;
  tmdbId: number;
  searchId: string;
  createdAt: number;
}>;

export const DISCOVERY_REVIEW_TTL = 5 * 60 * 1000;
export const isReviewUUID = (value: unknown): value is string =>
  typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value);
export const isReviewTMDBID = (value: unknown): value is number =>
  typeof value === "number" && Number.isSafeInteger(value) && value > 0;

export function currentDiscoveryReview(ref: DiscoveryReviewCandidateRef | null, userId: string | undefined, role: string | undefined, now = Date.now()): DiscoveryReviewCandidateRef | null {
  if (!ref || role !== "admin" || ref.userId !== userId ||
      ![ref.userId, ref.subscriptionId, ref.candidateId, ref.searchId].every(isReviewUUID) ||
      !isReviewTMDBID(ref.tmdbId) || !Number.isSafeInteger(ref.createdAt) ||
      ref.createdAt > now || now - ref.createdAt >= DISCOVERY_REVIEW_TTL) return null;
  return ref;
}

// List identity is authoritative; the older detail projection may omit tmdb_id.
export type DiscoveryReviewSubscription = {
  id: string; user_id: string; media_id: string; root_id: string;
  media_type?: string; tmdb_id?: number; state: string; intent_version: number;
  downloader_id?: string; quality_policy_id: string; quality_policy_revision: number;
  root_name?: string;
};
export function matchesDiscoveryTV(row: DiscoveryReviewSubscription, userId: string, tmdbId: number): boolean {
  return isReviewUUID(userId) && isReviewTMDBID(tmdbId) && row.user_id === userId &&
    row.media_type === "tv" && row.tmdb_id === tmdbId &&
    [row.id, row.media_id, row.root_id].every(isReviewUUID);
}

export function currentDiscoveryTVTarget(selected: DiscoveryReviewSubscription, detail: DiscoveryReviewSubscription, status: { id: string; intent_version: number; policy_revision: number; blockers: string[] }, userId: string, tmdbId: number): boolean {
  return matchesDiscoveryTV(selected, userId, tmdbId) &&
    detail.id === selected.id && detail.user_id === userId && detail.media_type === "tv" &&
    detail.media_id === selected.media_id && detail.root_id === selected.root_id &&
    (detail.tmdb_id === undefined || detail.tmdb_id === tmdbId) &&
    selected.state === "active" && detail.state === "active" &&
    Number.isSafeInteger(detail.intent_version) && detail.intent_version > 0 && detail.intent_version === selected.intent_version &&
    (detail.downloader_id === undefined || isReviewUUID(detail.downloader_id)) && detail.downloader_id === selected.downloader_id &&
    detail.quality_policy_id === selected.quality_policy_id &&
    Number.isSafeInteger(detail.quality_policy_revision) && detail.quality_policy_revision > 0 &&
    detail.quality_policy_revision === selected.quality_policy_revision &&
    status.id === selected.id && status.intent_version === detail.intent_version &&
    status.policy_revision === detail.quality_policy_revision && Array.isArray(status.blockers) &&
    status.blockers.every(value => typeof value === "string");
}
