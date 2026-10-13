import { fetchCities, fetchStates } from '../locationApi';

const respond = (status: number, body: unknown) =>
  Promise.resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as Response);

describe('locationApi', () => {
  const fetchMock = jest.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    global.fetch = fetchMock as unknown as typeof fetch;
  });

  it('reads states with a GET query, never the retired POST endpoint', async () => {
    fetchMock.mockReturnValue(respond(200, { data: { states: [{ name: 'Lagos State' }] } }));
    await expect(fetchStates('Nigeria (GET)')).resolves.toEqual(['Lagos State']);
    expect(fetchMock).toHaveBeenCalledWith('https://countriesnow.space/api/v0.1/countries/states/q?country=Nigeria+%28GET%29');
  });

  it('reads an unknown state as no cities, so the picker falls back to typing', async () => {
    fetchMock.mockReturnValue(respond(404, { error: true, msg: 'state not found' }));
    await expect(fetchCities('Nigeria', 'Fct')).resolves.toEqual([]);
  });

  it('throws on a server error so the picker offers a retry', async () => {
    fetchMock.mockReturnValue(respond(500, {}));
    await expect(fetchCities('Nigeria', 'Lagos State (500)')).rejects.toThrow();
  });
});
