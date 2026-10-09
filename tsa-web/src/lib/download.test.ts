import { downloadFile } from './download';

const createObjectURL = vi.fn(() => 'blob:csv');
const revokeObjectURL = vi.fn();
let saved: string[] = [];

const answer = (disposition?: string) =>
  vi.stubGlobal('fetch', vi.fn(async () => new Response('a\n', { status: 200, headers: disposition ? { 'Content-Disposition': disposition } : {} })));

beforeEach(() => {
  saved = [];
  createObjectURL.mockClear();
  revokeObjectURL.mockClear();
  Object.assign(URL, { createObjectURL, revokeObjectURL });
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { saved.push(this.download); });
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('downloadFile', () => {
  it('revokes the object URL only after the click has had time to start the download', async () => {
    answer();
    await downloadFile('/x', 'fallback.csv');
    expect(saved).toEqual(['fallback.csv']);
    expect(revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1000);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:csv');
    expect(document.querySelector('a[download]')).toBeNull();
  });

  it('keeps only the last path segment of the name the API gives, free of unsafe characters', async () => {
    answer('attachment; filename="../../evil\\x.csv"');
    await downloadFile('/x', 'fallback.csv');
    expect(saved).toEqual(['x.csv']);
  });

  it.each(['attachment; filename="report.exe"', 'attachment; filename="  .csv"', 'attachment; filename="a:b.txt"', 'attachment'])(
    'falls back when the name is odd (%s)', async (disposition) => {
      answer(disposition);
      await downloadFile('/x', 'fallback.csv');
      expect(saved).toEqual(['fallback.csv']);
    });

  it('a network error says it could not export, not the browser\'s own text', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Load failed'); }));
    await expect(downloadFile('/x', 'f.csv')).rejects.toThrow("Couldn't export. Try again.");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('a body cut off part-way (the API aborted the export) says it could not export', async () => {
    const cut = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('\uFEFFpaid_at\n'));
        controller.error(new TypeError('network error'));
      },
    });
    vi.stubGlobal('fetch', vi.fn(async () => new Response(cut, { status: 200 })));
    await expect(downloadFile('/x', 'f.csv')).rejects.toThrow("Couldn't export. Try again.");
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(saved).toEqual([]);
  });

  it('a file the browser cannot hold says it could not export', async () => {
    answer();
    createObjectURL.mockImplementationOnce(() => { throw new Error('out of memory'); });
    await expect(downloadFile('/x', 'f.csv')).rejects.toThrow("Couldn't export. Try again.");
    expect(saved).toEqual([]);
  });

  it('a click that throws still frees the object URL at once', async () => {
    answer();
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => { throw new Error('blocked'); });
    await expect(downloadFile('/x', 'f.csv')).rejects.toThrow('blocked');
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:csv');
    expect(document.querySelector('a[download]')).toBeNull();
  });
});
