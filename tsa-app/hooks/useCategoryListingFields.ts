// A category's listing fields, price units and booking mode, fetched once per
// category per app session — the add-listing form, the listing detail and the
// browse filters all ask for the same category's fields.
import { useEffect, useState } from 'react';
import { getCategoryListingFields } from '@/services/serviceListingApi';
import type { SchemaField } from '@/utils/schemaFields';

interface CategoryForm {
  fields: SchemaField[];
  // null = the category does not restrict price units.
  priceUnits: string[] | null;
  // null = not set; bookings then reserve time slots.
  bookingMode: 'slot' | 'date_range' | 'quantity' | null;
}

const EMPTY: CategoryForm = { fields: [], priceUnits: null, bookingMode: null };
const cache = new Map<string, CategoryForm>();

export function useCategoryListingFields(categoryId?: string | null): CategoryForm & {
  loading: boolean;
} {
  const cached = categoryId ? cache.get(categoryId) : undefined;
  const [form, setForm] = useState<CategoryForm>(cached ?? EMPTY);
  const [loading, setLoading] = useState(!!categoryId && !cached);

  useEffect(() => {
    if (!categoryId) {
      setForm(EMPTY);
      setLoading(false);
      return;
    }
    const hit = cache.get(categoryId);
    if (hit) {
      setForm(hit);
      setLoading(false);
      return;
    }
    let live = true;
    setLoading(true);
    getCategoryListingFields(categoryId).then((res) => {
      if (!live) return;
      // An older API without the endpoint simply means no category fields.
      const next: CategoryForm =
        res.success && res.data
          ? {
              fields: (res.data.listingFields as SchemaField[]) ?? [],
              priceUnits: res.data.resolvedConfig?.priceUnits?.value ?? null,
              bookingMode: res.data.resolvedConfig?.bookingMode?.value ?? null,
            }
          : EMPTY;
      if (res.success) cache.set(categoryId, next);
      setForm(next);
      setLoading(false);
    });
    return () => {
      live = false;
    };
  }, [categoryId]);

  return { ...form, loading };
}
