const BASE_URL = "https://countriesnow.space/api/v0.1";

const cache = new Map<string, string[]>();

export async function fetchStates(country: string): Promise<string[]> {
  const key = `states:${country}`;
  if (cache.has(key)) return cache.get(key)!;

  const response = await fetch(`${BASE_URL}/countries/states`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ country }),
  });

  if (!response.ok) throw new Error("Failed to fetch states");

  const json = await response.json();
  const states: string[] = (json.data?.states || []).map(
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

  const response = await fetch(`${BASE_URL}/countries/state/cities`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ country, state }),
  });

  if (!response.ok) throw new Error("Failed to fetch cities");

  const json = await response.json();
  const cities: string[] = json.data || [];
  cache.set(key, cities);
  return cities;
}
