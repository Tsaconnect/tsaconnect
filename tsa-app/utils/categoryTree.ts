// Category pickers show each category with its parents ("Accommodation ›
// Student hostel"), since the API returns a flat list and subcategories of
// different parents can share a name.

export interface FlatCategory {
  id: string;
  title: string;
  parentCategory?: string | null;
}

const SEP = ' › ';

// Path labels for every category, sorted so each parent precedes its
// children. A missing parent (filtered out, inactive) just starts the path.
export function categoryPathLabels<T extends FlatCategory>(categories: T[]): (T & { path: string })[] {
  const byId = new Map(categories.map((c) => [c.id, c]));
  const pathOf = (c: T): string => {
    const parts: string[] = [];
    const seen = new Set<string>();
    let cur: T | undefined = c;
    while (cur && !seen.has(cur.id)) {
      seen.add(cur.id);
      parts.unshift(cur.title);
      cur = cur.parentCategory ? byId.get(cur.parentCategory) : undefined;
    }
    return parts.join(SEP);
  };
  return categories
    .map((c) => ({ ...c, path: pathOf(c) }))
    .sort((a, b) => a.path.localeCompare(b.path));
}
