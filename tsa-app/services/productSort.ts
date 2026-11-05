// services/productSort.ts
//
// How a product list can be sorted, in one place: the options the picker
// shows, what each asks the API for, and the same order applied on the device
// to a list the API returns unsorted (a whole category tree).

import type { Product } from '@/components/services/api';

export type ProductSort = 'recent' | 'nearest' | 'popular' | 'price_low' | 'price_high' | 'rating';

export const PRODUCT_SORTS: { id: ProductSort; label: string }[] = [
  { id: 'recent', label: 'Newest' },
  { id: 'nearest', label: 'Nearest to me' },
  { id: 'popular', label: 'Most popular' },
  { id: 'price_low', label: 'Price: low to high' },
  { id: 'price_high', label: 'Price: high to low' },
  { id: 'rating', label: 'Top rated' },
];

export const productSortLabel = (sort: ProductSort): string =>
  PRODUCT_SORTS.find((s) => s.id === sort)?.label ?? 'Newest';

/** The user's own place, from their profile. */
export interface NearPlace {
  country?: string;
  state?: string;
  city?: string;
}

/** What GET /products/public/category is asked for. Only a low-to-high price
 *  sort is ascending: newest, most popular and top rated come first. */
export function serverSort(sort: ProductSort): { sortBy: string; sortOrder: 'asc' | 'desc' } {
  switch (sort) {
    case 'nearest':
      return { sortBy: 'nearest', sortOrder: 'desc' };
    case 'popular':
      return { sortBy: 'sales', sortOrder: 'desc' };
    case 'price_low':
      return { sortBy: 'price', sortOrder: 'asc' };
    case 'price_high':
      return { sortBy: 'price', sortOrder: 'desc' };
    case 'rating':
      return { sortBy: 'rating.average', sortOrder: 'desc' };
    default:
      return { sortBy: 'createdAt', sortOrder: 'desc' };
  }
}

/** The API's LocationKey: "Enugu", "enugu state" and "Enugu State." match. */
export function locationKey(s?: string): string {
  return (s ?? '')
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .join(' ')
    .replace(/[.\s]+$/, '')
    .replace(/ state$/, '');
}

export const hasPlace = (near?: NearPlace | null): boolean =>
  !!near && !!(locationKey(near.country) || locationKey(near.state) || locationKey(near.city));

/** 0 same city (within the state), 1 same state, 2 same country, 3 the rest —
 *  as the API ranks it. */
function closeness(p: any, near: NearPlace): number {
  const country = locationKey(near.country);
  const state = locationKey(near.state);
  const city = locationKey(near.city);
  const pState = locationKey(p?.state);
  if (city && locationKey(p?.city) === city && (!state || pState === state)) return 0;
  if (state && pState === state) return 1;
  if (country && locationKey(p?.country) === country) return 2;
  return 3;
}

const time = (p: any) => Date.parse(p?.createdAt ?? p?.created_at ?? '') || 0;
// Plain code-unit order, as PostgreSQL compares a uuid (localeCompare may skip the dashes).
const byIdDesc = (a: any, b: any) => {
  const x = String(a?.id ?? ''), y = String(b?.id ?? '');
  return x === y ? 0 : x < y ? 1 : -1;
};
const num = (v: unknown) => (typeof v === 'number' ? v : Number(v) || 0);

/** Sorts a list on the device the way the API would. */
export function sortProducts(products: Product[], sort: ProductSort, near?: NearPlace | null): Product[] {
  // id breaks ties, as the API's ORDER BY does.
  const newest = (a: any, b: any) => time(b) - time(a) || byIdDesc(a, b);
  const by: Record<ProductSort, (a: any, b: any) => number> = {
    recent: newest,
    nearest: (a, b) => (near ? closeness(a, near) - closeness(b, near) : 0) || newest(a, b),
    popular: (a, b) => num(b?.sales) - num(a?.sales) || newest(a, b),
    price_low: (a, b) => num(a?.price) - num(b?.price),
    price_high: (a, b) => num(b?.price) - num(a?.price),
    rating: (a, b) => num(b?.rating?.average) - num(a?.rating?.average) || newest(a, b),
  };
  return [...products].sort(by[sort]);
}
