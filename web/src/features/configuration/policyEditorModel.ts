import type { components } from "@/api/schema";
export type PolicyRules = components["schemas"]["PolicyRuleSet"];
export type CustomWord = components["schemas"]["QualityWordRule"];
const record = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

export function clonePolicyRules(rules: PolicyRules): PolicyRules {
  const copy = { ...rules, resolution_order: { ...rules.resolution_order }, source_order: { ...rules.source_order }, reject_terms: [...rules.reject_terms], reject_groups: [...rules.reject_groups] };
  if (rules.custom_words !== undefined) copy.custom_words = rules.custom_words.map(word => ({ ...word }));
  return copy;
}

export function preferenceScore(text: string): number | null {
  if (!/^(0|-?[1-9][0-9]{0,3})$/.test(text)) return null;
  const score = Number(text);
  return Number.isSafeInteger(score) && score >= -1000 && score <= 1000 ? score : null;
}

export function validCustomWords(value: unknown): value is CustomWord[] {
  if (!Array.isArray(value) || value.length > 64) return false;
  const seen = new Set<string>();
  for (const row of value) {
    if (!record(row) || Object.keys(row).length !== 4 || !["word", "score", "required", "excluded"].every(key => Object.hasOwn(row, key))) return false;
    if (typeof row.word !== "string" || !row.word || row.word.trim() !== row.word || [...row.word].length > 128 || /[\p{Cc}\p{Cf}\p{Cs}]/u.test(row.word)) return false;
    if (!Number.isSafeInteger(row.score) || typeof row.score !== "number" || row.score < -1000 || row.score > 1000 || typeof row.required !== "boolean" || typeof row.excluded !== "boolean") return false;
    if (row.required && row.excluded || row.excluded && row.score !== 0 || !row.required && !row.excluded && row.score === 0) return false;
    // Go strings.ToLower uses simple per-codepoint lowercasing, including U+0130.
    const key = [...row.word].map(c => c === "İ" ? "i" : c.toLowerCase()).join("");
    if (seen.has(key)) return false;
    seen.add(key);
  }
  return true;
}

export function validPolicyRules(value: unknown): value is PolicyRules {
  if (!record(value)) return false;
  const order = (v: unknown) => record(v) && Object.values(v).every(Number.isSafeInteger);
  const terms = (v: unknown) => Array.isArray(v) && v.every(x => typeof x === "string");
  return order(value.resolution_order) && order(value.source_order) && terms(value.reject_terms) && terms(value.reject_groups) && Number.isSafeInteger(value.cutoff_score) && typeof value.prefer_hdr === "boolean" && (value.custom_words === undefined || validCustomWords(value.custom_words));
}
