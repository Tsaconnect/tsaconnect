// A category's listing fields, fetched once per category per app session —
// the add-listing form, the listing detail and the browse filters all ask for
// the same category's fields.
import { useEffect, useState } from 'react';
import { getCategoryListingFields } from '@/services/serviceListingApi';
import type { SchemaField } from '@/utils/schemaFields';

const cache = new Map<string, SchemaField[]>();

export function useCategoryListingFields(categoryId?: string | null): {
  fields: SchemaField[];
  loading: boolean;
} {
  const cached = categoryId ? cache.get(categoryId) : undefined;
  const [fields, setFields] = useState<SchemaField[]>(cached ?? []);
  const [loading, setLoading] = useState(!!categoryId && !cached);

  useEffect(() => {
    if (!categoryId) {
      setFields([]);
      setLoading(false);
      return;
    }
    const hit = cache.get(categoryId);
    if (hit) {
      setFields(hit);
      setLoading(false);
      return;
    }
    let live = true;
    setLoading(true);
    getCategoryListingFields(categoryId).then((res) => {
      if (!live) return;
      // An older API without the endpoint simply means no category fields.
      const next = res.success && res.data ? (res.data.listingFields as SchemaField[]) ?? [] : [];
      if (res.success) cache.set(categoryId, next);
      setFields(next);
      setLoading(false);
    });
    return () => {
      live = false;
    };
  }, [categoryId]);

  return { fields, loading };
}
