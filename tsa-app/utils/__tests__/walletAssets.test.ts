import {
  ASSET_SOURCES,
  allSourcesReported,
  combineSourceAssets,
  emptySourceAssets,
  isWalletSnapshot,
  type SourceAssets,
} from '../walletAssets';

type Row = { id: string };
const row = (id: string): Row => ({ id });

describe('emptySourceAssets', () => {
  it('starts every source unreported', () => {
    const sources = emptySourceAssets<Row>();
    for (const source of ASSET_SOURCES) {
      expect(sources[source]).toBeNull();
    }
  });

  it('returns a fresh object each call', () => {
    const a = emptySourceAssets<Row>();
    a.evm = [row('leak')];
    expect(emptySourceAssets<Row>().evm).toBeNull();
  });
});

describe('combineSourceAssets', () => {
  it('renders sources in a stable order regardless of arrival order', () => {
    // The order rows land in is the order the network answers in, which varies
    // run to run. The list the user sees must not.
    const sources = emptySourceAssets<Row>();
    sources.premium = [row('premium')];
    sources.btc = [row('btc')];
    sources.evm = [row('evm')];

    expect(combineSourceAssets(sources).map(r => r.id)).toEqual(['evm', 'btc', 'premium']);
  });

  it('includes sources that report late', () => {
    // The bug this guards: the combine used to read React state that had not
    // committed yet, so the last source to land was silently dropped.
    const sources = emptySourceAssets<Row>();
    sources.evm = [row('evm')];
    expect(combineSourceAssets(sources)).toHaveLength(1);

    sources.solana = [row('solana')];
    expect(combineSourceAssets(sources).map(r => r.id)).toEqual(['evm', 'solana']);
  });

  it('treats an unreported source and one holding nothing the same way in output', () => {
    const unreported = emptySourceAssets<Row>();
    unreported.evm = [row('evm')];

    const reportedEmpty = emptySourceAssets<Row>();
    reportedEmpty.evm = [row('evm')];
    reportedEmpty.btc = [];

    expect(combineSourceAssets(unreported)).toEqual(combineSourceAssets(reportedEmpty));
  });

  it('appends extras after every source row', () => {
    const sources = emptySourceAssets<Row>();
    sources.evm = [row('evm')];
    sources.premium = [row('premium')];

    const combined = combineSourceAssets(sources, [row('imported'), row('discovered')]);

    expect(combined.map(r => r.id)).toEqual(['evm', 'premium', 'imported', 'discovered']);
  });

  it('keeps source rows when there are no extras', () => {
    const sources = emptySourceAssets<Row>();
    sources.evm = [row('evm')];
    expect(combineSourceAssets(sources, []).map(r => r.id)).toEqual(['evm']);
  });

  it('returns nothing when no source has reported', () => {
    expect(combineSourceAssets(emptySourceAssets<Row>())).toEqual([]);
  });

  it('does not mutate the sources it is given', () => {
    const sources = emptySourceAssets<Row>();
    sources.evm = [row('evm')];
    combineSourceAssets(sources, [row('extra')]);
    expect(sources.evm).toEqual([row('evm')]);
  });
});

describe('allSourcesReported', () => {
  it('is false while any source is outstanding', () => {
    const sources = emptySourceAssets<Row>();
    for (const source of ASSET_SOURCES) sources[source] = [];
    sources.btc = null;

    expect(allSourcesReported(sources)).toBe(false);
  });

  it('is true once every source has reported, including empty ones', () => {
    // A chain the user holds nothing on still counts as reported — otherwise
    // the total would shimmer forever for anyone without, say, Bitcoin.
    const sources: SourceAssets<Row> = emptySourceAssets<Row>();
    for (const source of ASSET_SOURCES) sources[source] = [];

    expect(allSourcesReported(sources)).toBe(true);
  });
});

describe('isWalletSnapshot', () => {
  const complete = () => {
    const sources = emptySourceAssets<Row>();
    for (const source of ASSET_SOURCES) sources[source] = [row(source)];
    return { sources, extras: [row('x')] };
  };

  it('accepts a saved list, including a source that had not reported', () => {
    expect(isWalletSnapshot(complete())).toBe(true);
    expect(isWalletSnapshot({ ...complete(), sources: emptySourceAssets<Row>() })).toBe(true);
  });

  it('rejects anything a later app version or a corrupt write could leave', () => {
    expect(isWalletSnapshot(null)).toBe(false);
    expect(isWalletSnapshot({ sources: complete().sources })).toBe(false);
    expect(isWalletSnapshot({ extras: [] })).toBe(false);
    const { tron: _tron, ...missingTron } = complete().sources;
    expect(isWalletSnapshot({ sources: missingTron, extras: [] })).toBe(false);
    expect(isWalletSnapshot({ sources: { ...complete().sources, btc: 'oops' }, extras: [] })).toBe(false);
  });
});
