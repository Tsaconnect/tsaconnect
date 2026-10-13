const BASE_URL = "https://countriesnow.space/api/v0.1";

// countriesnow retired its POST endpoints (they 301 to these GET ones, and a
// redirected POST loses its body). A 404 is "no such place in their data":
// an empty list, so the picker falls back to typing, never a retry loop.
async function getList(path: string, params: Record<string, string>): Promise<unknown | null> {
  const response = await fetch(`${BASE_URL}${path}?${new URLSearchParams(params).toString()}`);
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`Failed to fetch ${path}`);
  return response.json();
}

const cache = new Map<string, string[]>();

export async function fetchStates(country: string): Promise<string[]> {
  const key = `states:${country}`;
  if (cache.has(key)) return cache.get(key)!;

  const json: any = await getList("/countries/states/q", { country });
  const states: string[] = (json?.data?.states || []).map(
    (s: { name: string }) => s.name
  );
  cache.set(key, states);
  return states;
}

export async function fetchCities(
  country: string,
  state: string
): Promise<string[]> {
  const key = `cities:${country}:${state}`;
  if (cache.has(key)) return cache.get(key)!;

  const json: any = await getList("/countries/state/cities/q", { country, state });
  const cities: string[] = Array.isArray(json?.data) ? json.data : [];
  cache.set(key, cities);
  return cities;
}
