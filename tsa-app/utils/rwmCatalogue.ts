// utils/rwmCatalogue.ts
//
// Wiring for the Real World Marketplace card's catalogue tabs: which service
// type each tab browses, what its row button says, and where a tapped row goes.
//
// The marketplace stores products and services in one collection, and the
// listing's own `type` is merchant-entered — flights and hairdressing routinely
// arrive as type "Product". The CATEGORY is the reliable classifier, which the
// API now applies server-side (see models.ServiceCategoryIDs). Here the active
// TAB is authoritative: a row shown under Booking is a service, whatever the
// listing claims about itself.
import type { Product } from '@/components/services/api';

export type ServiceType = 'contact' | 'order' | 'booking';
export type RwmTabKey = 'orders' | 'products' | 'contact' | 'ordering' | 'booking';

/** Category serviceType each catalogue tab browses. */
export const SERVICE_TAB_TYPE: Record<'contact' | 'ordering' | 'booking', ServiceType> = {
  contact: 'contact',
  ordering: 'order',
  booking: 'booking',
};

/** Row button per tab. Orders is history, so it has no call to action. */
export const CTA_LABEL: Record<RwmTabKey, string | null> = {
  orders: null,
  products: 'Buy',
  contact: 'Contact',
  ordering: 'Order',
  booking: 'Book',
};

export interface RwmTarget {
  kind: 'product' | 'service' | 'order';
  id: string;
  title: string;
  description?: string;
  image?: string;
}

/**
 * The section a listing belongs to — the category title ("Flights", "Hair
 * Extensions") the PM asked to see tagged on every row. Falls back to the
 * location so a category-less listing still says something useful.
 */
export function listingSection(p: Product): string | undefined {
  const fromCategory =
    typeof p.category === 'object' && p.category ? p.category.title : p.categoryName;
  return fromCategory || (p as { location?: string }).location || undefined;
}

/** Where a listing row goes, decided by the tab it was shown under. */
export function listingTarget(p: Product, tab: Exclude<RwmTabKey, 'orders'>): RwmTarget {
  return {
    kind: tab === 'products' ? 'product' : 'service',
    id: p.id || p._id || '',
    title: p.name || 'Listing',
    description: p.description || undefined,
    image: p.images?.[0]?.url || undefined,
  };
}

/** Anything accepted by router.push — a plain path or a path + params. */
export type RwmHref = string | { pathname: string; params: Record<string, string> };

/** Router argument for a row target. */
export function targetHref(target: RwmTarget): RwmHref {
  if (target.kind === 'product') return `/product/${target.id}`;
  if (target.kind === 'order') return { pathname: '/orderdetail', params: { id: target.id } };

  // servicedetail renders from params before its own fetch resolves, so pass
  // what we have — but omit absent keys, or the screen renders "undefined".
  const params: Record<string, string> = { id: target.id, title: target.title };
  if (target.description) params.description = target.description;
  if (target.image) params.image = target.image;
  return { pathname: '/servicedetail', params };
}

/** "View all" destination per tab. */
export function viewAllHref(tab: RwmTabKey): RwmHref {
  if (tab === 'orders') return '/orderlist';
  if (tab === 'products') return '/products';
  return {
    pathname: '/products',
    params: { tab: 'services', serviceType: SERVICE_TAB_TYPE[tab] },
  };
}
