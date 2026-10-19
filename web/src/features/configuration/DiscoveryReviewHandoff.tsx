import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { useAuth } from "@/features/auth/AuthContext";
import { currentDiscoveryReview, DISCOVERY_REVIEW_TTL, type DiscoveryReviewCandidateRef } from "./discoveryReviewHandoff";
export type { DiscoveryReviewCandidateRef } from "./discoveryReviewHandoff";

type Handoff = {
  readonly candidate: DiscoveryReviewCandidateRef | null;
  offer(ref: DiscoveryReviewCandidateRef): boolean;
  clear(): void;
};
const DiscoveryReviewContext = createContext<Handoff | null>(null);

export function DiscoveryReviewHandoffProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const identity = `${user?.id ?? ""}:${user?.role ?? ""}`;
  const state = useRef({ identity, epoch: 0, candidate: null as DiscoveryReviewCandidateRef | null });
  const [, changed] = useState(0);
  const mounted = useRef(true);
  // Clear during render so children and retained callbacks cannot read the previous account.
  if (state.current.identity !== identity) {
    state.current = { identity, epoch: state.current.epoch + 1, candidate: null };
  }
  const epoch = state.current.epoch;
  const candidate = currentDiscoveryReview(state.current.candidate, user?.id, user?.role);
  if (!candidate) state.current.candidate = null;
  useEffect(() => {
    if (!candidate) return;
    const timer = setTimeout(() => {
      if (state.current.epoch === epoch && state.current.candidate === candidate) {
        state.current.candidate = null;
        changed(value => value + 1);
      }
    }, Math.max(0, candidate.createdAt + DISCOVERY_REVIEW_TTL - Date.now()));
    return () => clearTimeout(timer);
  }, [candidate, epoch]);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; state.current.candidate = null; };
  }, []);
  const handoff: Handoff = {
    get candidate() {
      return mounted.current && state.current.epoch === epoch ? currentDiscoveryReview(state.current.candidate, user?.id, user?.role) : null;
    },
    offer(ref) {
      if (!mounted.current || state.current.epoch !== epoch || !currentDiscoveryReview(ref, user?.id, user?.role)) return false;
      state.current.candidate = Object.freeze({ userId: ref.userId, subscriptionId: ref.subscriptionId, candidateId: ref.candidateId, tmdbId: ref.tmdbId, searchId: ref.searchId, createdAt: ref.createdAt });
      changed(value => value + 1);
      return true;
    },
    clear() {
      if (!mounted.current || state.current.epoch !== epoch) return;
      state.current.candidate = null;
      changed(value => value + 1);
    },
  };
  return <DiscoveryReviewContext.Provider value={handoff}>{children}</DiscoveryReviewContext.Provider>;
}

export function useDiscoveryReviewHandoff(): Handoff {
  const context = useContext(DiscoveryReviewContext);
  if (!context) throw new Error("useDiscoveryReviewHandoff requires DiscoveryReviewHandoffProvider inside AuthProvider");
  return context;
}
