import { put } from "../api";

// Tags typed anywhere in the console are created when they are first used,
// so nobody has to visit the Servers page first.

const tagNameRe = /^tag:[a-z0-9][a-z0-9-]{0,62}$/;

export function normalizeTag(v: string): string {
  const t = v.trim().toLowerCase();
  return t.startsWith("tag:") ? t : `tag:${t}`;
}

export function validTag(v: string): string | null {
  return tagNameRe.test(normalizeTag(v)) ? null : `"${v}": a tag uses lowercase letters, digits and dashes, like web-servers`;
}

// ensureTags creates the tags in names that do not exist yet.
export async function ensureTags(names: string[], known: string[]): Promise<void> {
  for (const n of names) {
    if (!known.includes(n)) await put(`/admin/api/tags/${encodeURIComponent(n)}`, { description: "", access: [] });
  }
}

export function newTagsHint(names: string[], known: string[]): string | undefined {
  const fresh = names.filter((n) => !known.includes(n));
  if (!fresh.length) return undefined;
  return `${fresh.join(", ")} ${fresh.length === 1 ? "is a new tag" : "are new tags"}, created when you save. Nobody can reach a new tag's servers until you give access on the Servers page.`;
}
