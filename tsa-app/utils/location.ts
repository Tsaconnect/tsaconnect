// utils/location.ts
//
// The country/state/city a LocationPicker edits, the area typed beside it, and
// how a listing stores them: one "city, state, country" display string plus the
// structured parts the API searches on (tsa-api-go models/listing_location.go —
// keep parseLocation reading the string the same way ParseListingLocation does).
export interface LocationValue {
  country: string;
  state: string;
  city: string;
}

const EMPTY: LocationValue = { country: '', state: '', city: '' };

// A listing stores its location as one "city, state, country" string.
export function formatLocation(location: LocationValue): string {
  return [location.city, location.state, location.country]
    .map((p) => p.trim())
    .filter(Boolean)
    .join(', ');
}

// The inverse of formatLocation, read from the end: the last part is the
// country, the one before it the state, and everything before that the city (a
// typed city may contain commas). A string with no comma ("Enugu Nigeria")
// cannot be split safely and reads as empty, so the picker starts blank.
export function parseLocation(saved: string | null | undefined): LocationValue {
  const parts = (saved || '')
    .split(',')
    .map((p) => p.trim().replace(/\s+/g, ' '))
    .filter(Boolean);
  if (parts.length < 2) return { ...EMPTY };
  const n = parts.length;
  return { city: parts.slice(0, n - 2).join(', '), state: parts[n - 2], country: parts[n - 1] };
}

// A saved listing's place: its structured parts when the API has them, else
// parsed from the display string (listings saved before the parts existed).
export function listingLocation(listing: {
  location?: string | null;
  country?: string | null;
  state?: string | null;
  city?: string | null;
  area?: string | null;
}): { value: LocationValue; area: string } {
  const area = listing.area ?? '';
  if (listing.country || listing.state || listing.city) {
    return {
      value: { country: listing.country ?? '', state: listing.state ?? '', city: listing.city ?? '' },
      area,
    };
  }
  return { value: parseLocation(listing.location), area };
}

// Adds a listing's place to a create/update form: the display string and the
// parts the API stores and searches on.
export function appendListingLocation(formData: FormData, value: LocationValue, area: string): void {
  formData.append('location', formatLocation(value));
  formData.append('country', value.country.trim());
  formData.append('state', value.state.trim());
  formData.append('city', value.city.trim());
  formData.append('area', area.trim());
}

// A listing's place as one line for cards and details: "Kubwa · Kubwa, FCT, Nigeria".
export function locationLabel(listing: { location?: string | null; area?: string | null }): string {
  return [listing.area?.trim(), listing.location?.trim()].filter(Boolean).join(' · ');
}
