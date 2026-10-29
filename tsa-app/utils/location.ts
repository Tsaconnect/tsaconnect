// utils/location.ts
//
// The country/state/city a LocationPicker edits, and the single string a
// listing stores it as.
export interface LocationValue {
  country: string;
  state: string;
  city: string;
}

// A listing stores its location as one "city, state, country" string.
export function formatLocation(location: LocationValue): string {
  return [location.city, location.state, location.country].filter(Boolean).join(', ');
}

// The inverse of formatLocation. Parts are read from the end, so a saved
// "state, country" still fills country and state and leaves city empty.
export function parseLocation(saved: string | null | undefined): LocationValue {
  const parts = (saved || '').split(',').map((p) => p.trim()).filter(Boolean);
  return {
    city: parts.length >= 3 ? parts[0] : '',
    state: parts.length >= 2 ? parts[parts.length - 2] : '',
    country: parts.length >= 1 ? parts[parts.length - 1] : '',
  };
}
