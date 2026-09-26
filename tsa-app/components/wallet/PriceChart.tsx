// components/wallet/PriceChart.tsx
//
// Token price chart for the token detail screen: line or candles, five ranges,
// drag horizontally to scrub (a vertical drag still scrolls the page). Line data
// comes from /market/history, candles from /market/ohlc. The line/candle choice
// is remembered across screens. Maths lives in utils/priceChart.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { LayoutChangeEvent, PanResponder, View } from 'react-native';
import Svg, { Circle, Defs, G, Line, LinearGradient, Path, Rect, Stop, Text as SvgText } from 'react-native-svg';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useTheme } from '@/theme';
import { Icon, PressableScale, SegmentedControl, Spinner, Text } from '@/components/ui';
import { getTokenOHLC, getTokenPriceHistory } from '@/services/walletApi';
import { formatUSD } from '@/utils/formatPrice';
import {
  CHART_RANGES,
  chartFetchOutcome,
  formatChartTime,
  indexAtX,
  pctChange,
  priceDomain,
  priceFractionDigits,
  type Candle,
  type ChartFetchOutcome,
  type ChartMode,
} from '@/utils/priceChart';

const CHART_HEIGHT = 200;
const EMPTY_HEIGHT = 96;
const PAD_TOP = 18;
const PAD_BOTTOM = 18;
const OHLC_ROW_HEIGHT = 18;
const LINE_POINTS = 120;
const MAX_CANDLES = 48;
const MODE_KEY = 'token_chart_mode';

const MODE_SEGMENTS: { key: ChartMode; label: string }[] = [
  { key: 'line', label: 'Line' },
  { key: 'candle', label: 'Candles' },
];

const formatPrice = (v: number) => formatUSD(v, { maxDecimals: priceFractionDigits(v) });

type SeriesResult = { series: Candle[]; outcome: ChartFetchOutcome };

async function fetchSeries(symbol: string, mode: ChartMode, days: number): Promise<SeriesResult> {
  if (mode === 'candle') {
    const res = await getTokenOHLC(symbol, days, MAX_CANDLES);
    const series = res.success && Array.isArray(res.data) ? res.data : [];
    return { series, outcome: chartFetchOutcome(res, series.length) };
  }
  const res = await getTokenPriceHistory(symbol, days, LINE_POINTS);
  // Both modes render from candles: a line point is a candle whose OHLC are all its price.
  const series =
    res.success && Array.isArray(res.data)
      ? res.data.map((p) => ({ timestamp: p.timestamp, open: p.price, high: p.price, low: p.price, close: p.price }))
      : [];
  return { series, outcome: chartFetchOutcome(res, series.length) };
}

export default function PriceChart({ symbol }: { symbol: string }) {
  const { colors, spacing, radius, fonts } = useTheme();
  // null until the saved choice is read, so a candle user never fetches line data first.
  const [savedMode, setSavedMode] = useState<ChartMode | null>(null);
  const mode: ChartMode = savedMode ?? 'line';
  const [days, setDays] = useState<number>(CHART_RANGES[0].days);
  // Data is tagged with the request it answers; a stale answer never renders under a new mode/range.
  const [loaded, setLoaded] = useState<({ key: string } & SeriesResult) | null>(null);
  const [retryCount, setRetryCount] = useState(0);
  const [width, setWidth] = useState(0);
  const [active, setActive] = useState<number | null>(null);
  const cache = useRef(new Map<string, Candle[]>());

  useEffect(() => {
    AsyncStorage.getItem(MODE_KEY)
      .then((saved) => setSavedMode(saved === 'candle' ? 'candle' : 'line'))
      .catch(() => setSavedMode('line'));
  }, []);

  const changeMode = useCallback((next: ChartMode) => {
    setSavedMode(next);
    AsyncStorage.setItem(MODE_KEY, next).catch(() => {});
  }, []);

  const requestKey = `${symbol}:${mode}:${days}`;
  useEffect(() => {
    if (savedMode == null) return;
    setActive(null);
    const hit = cache.current.get(requestKey);
    if (hit) {
      setLoaded({ key: requestKey, series: hit, outcome: 'ok' });
      return;
    }
    let cancelled = false;
    setLoaded(null);
    fetchSeries(symbol, savedMode, days).then((result) => {
      if (cancelled) return;
      if (result.outcome === 'ok') cache.current.set(requestKey, result.series);
      setLoaded({ key: requestKey, ...result });
    });
    return () => {
      cancelled = true;
    };
  }, [symbol, savedMode, days, requestKey, retryCount]);

  const loading = loaded?.key !== requestKey;
  const series = useMemo(() => (loading || !loaded ? [] : loaded.series), [loading, loaded]);
  const n = series.length;
  const hasData = n >= 2;
  const open = hasData ? series[0].open : 0;
  const last = hasData ? series[n - 1].close : 0;
  const trendColor = pctChange(open, last) >= 0 ? colors.success : colors.danger;

  // Everything that does not move while scrubbing: geometry and the drawn series.
  const chart = useMemo(() => {
    if (!hasData || width <= 0) return null;
    const values = series.flatMap((c) => (mode === 'candle' ? [c.low, c.high] : [c.close]));
    const { lo, hi } = priceDomain([...values, open]);
    const x = (i: number) => (mode === 'candle' ? ((i + 0.5) * width) / n : (i * width) / (n - 1));
    const y = (v: number) => PAD_TOP + ((hi - v) / (hi - lo)) * (CHART_HEIGHT - PAD_TOP - PAD_BOTTOM);
    const highIdx = series.reduce((m, c, i) => (c.high > series[m].high ? i : m), 0);
    const lowIdx = series.reduce((m, c, i) => (c.low < series[m].low ? i : m), 0);

    const marker = (i: number, v: number, below: boolean) => {
      const cx = x(i);
      const anchor = cx < width * 0.2 ? 'start' : cx > width * 0.8 ? 'end' : 'middle';
      return (
        <SvgText
          x={cx}
          y={y(v) + (below ? 13 : -6)}
          fontSize={10}
          fontFamily={fonts['600']}
          fill={colors.textSecondary}
          textAnchor={anchor}
        >
          {formatPrice(v)}
        </SvgText>
      );
    };

    let plot: React.ReactNode;
    if (mode === 'candle') {
      const bodyW = Math.max(2, (width / n) * 0.6);
      plot = series.map((c, i) => {
        const fill = c.close >= c.open ? colors.success : colors.danger;
        return (
          <G key={i}>
            <Line x1={x(i)} x2={x(i)} y1={y(c.high)} y2={y(c.low)} stroke={fill} strokeWidth={1} />
            <Rect
              x={x(i) - bodyW / 2}
              y={y(Math.max(c.open, c.close))}
              width={bodyW}
              height={Math.max(1, Math.abs(y(c.open) - y(c.close)))}
              rx={1}
              fill={fill}
            />
          </G>
        );
      });
    } else {
      const d = series.map((c, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(c.close).toFixed(1)}`).join('');
      const bottom = CHART_HEIGHT - PAD_BOTTOM;
      plot = (
        <>
          <Defs>
            <LinearGradient id="priceFill" x1="0" y1="0" x2="0" y2="1">
              <Stop offset="0" stopColor={trendColor} stopOpacity={0.22} />
              <Stop offset="1" stopColor={trendColor} stopOpacity={0} />
            </LinearGradient>
          </Defs>
          <Path d={`${d}L${x(n - 1)} ${bottom}L${x(0)} ${bottom}Z`} fill="url(#priceFill)" />
          <Path d={d} fill="none" stroke={trendColor} strokeWidth={2.2} strokeLinejoin="round" strokeLinecap="round" />
        </>
      );
    }

    const layer = (
      <>
        <Line
          x1={0}
          x2={width}
          y1={y(open)}
          y2={y(open)}
          stroke={colors.textMuted}
          strokeDasharray="3 4"
          strokeWidth={1}
          opacity={0.6}
        />
        {plot}
        {marker(highIdx, series[highIdx].high, false)}
        {marker(lowIdx, series[lowIdx].low, true)}
      </>
    );
    return { x, y, layer, high: series[highIdx].high, low: series[lowIdx].low };
  }, [hasData, width, series, mode, open, n, trendColor, colors, fonts]);

  // PanResponder is created once; it reads the latest layout through this ref.
  const scrub = useRef({ width, n, mode });
  scrub.current = { width, n, mode };
  const pan = useMemo(
    () =>
      PanResponder.create({
        onMoveShouldSetPanResponder: (_, g) => Math.abs(g.dx) > 6 && Math.abs(g.dx) > Math.abs(g.dy),
        onPanResponderGrant: (e) => {
          const s = scrub.current;
          setActive(indexAtX(e.nativeEvent.locationX, s.width, s.n, s.mode));
        },
        onPanResponderMove: (e) => {
          const s = scrub.current;
          setActive(indexAtX(e.nativeEvent.locationX, s.width, s.n, s.mode));
        },
        onPanResponderTerminationRequest: () => false,
        onPanResponderRelease: () => setActive(null),
        onPanResponderTerminate: () => setActive(null),
      }),
    [],
  );

  const activeIdx = active != null && hasData ? Math.min(active, n - 1) : null;
  const shown = activeIdx != null ? series[activeIdx] : null;
  const shownPrice = shown ? shown.close : last;
  const change = pctChange(open, shownPrice);
  const changeColor = change >= 0 ? colors.success : colors.danger;
  const digits = priceFractionDigits(shownPrice || open);
  const rangeLabel = CHART_RANGES.find((r) => r.days === days)?.label ?? '';

  const onLayout = (e: LayoutChangeEvent) => setWidth(e.nativeEvent.layout.width);

  const renderBody = () => {
    if (loading || savedMode == null) {
      return (
        <View style={{ height: CHART_HEIGHT, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner color={colors.textMuted} />
        </View>
      );
    }
    if (!hasData) {
      const failed = loaded?.outcome === 'failed';
      return (
        <PressableScale
          disabled={!failed}
          onPress={() => setRetryCount((c) => c + 1)}
          accessibilityRole={failed ? 'button' : undefined}
          style={{
            height: EMPTY_HEIGHT,
            alignItems: 'center',
            justifyContent: 'center',
            gap: 6,
            backgroundColor: colors.surfaceAlt,
            borderRadius: radius.sm,
          }}
        >
          <Icon name={failed ? 'refresh' : 'trendingUp'} size={20} color={colors.textMuted} />
          <Text variant="bodySm" color="textMuted">
            {failed ? "Couldn't load the chart. Tap to retry" : `No price data for ${symbol}`}
          </Text>
        </PressableScale>
      );
    }
    if (!chart) return <View style={{ height: CHART_HEIGHT }} />;
    return (
      <View {...pan.panHandlers} style={{ height: CHART_HEIGHT }}>
        {/* pointerEvents none keeps the touch target on this View, so locationX is chart-relative */}
        <Svg width={width} height={CHART_HEIGHT} pointerEvents="none">
          {chart.layer}
          {shown && activeIdx != null ? (
            <>
              <Line
                x1={chart.x(activeIdx)}
                x2={chart.x(activeIdx)}
                y1={PAD_TOP - 8}
                y2={CHART_HEIGHT - PAD_BOTTOM}
                stroke={colors.textMuted}
                strokeWidth={1}
              />
              <Circle
                cx={chart.x(activeIdx)}
                cy={chart.y(shown.close)}
                r={5}
                fill={trendColor}
                stroke={colors.surface}
                strokeWidth={2}
              />
            </>
          ) : (
            mode === 'line' && <Circle cx={chart.x(n - 1)} cy={chart.y(last)} r={4} fill={trendColor} />
          )}
        </Svg>
      </View>
    );
  };

  return (
    <View>
      {/* Header: price at the scrubbed point (or latest) and change from the range's open */}
      <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', minHeight: 68 }}>
        <View style={{ flex: 1, paddingRight: spacing.sm }}>
          <Text variant="overline" color="textMuted">
            {shown ? formatChartTime(shown.timestamp, days) : `${symbol} price`.toUpperCase()}
          </Text>
          {hasData && (
            <>
              <Text variant="title2" weight="700" numeric>
                {formatUSD(shownPrice, { maxDecimals: digits })}
              </Text>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name={change >= 0 ? 'trendingUp' : 'trendingDown'} size={14} color={changeColor} />
                <Text variant="caption" weight="600" numeric style={{ color: changeColor }}>
                  {change >= 0 ? '+' : ''}
                  {change.toFixed(2)}%
                </Text>
                <Text variant="caption" color="textMuted">
                  {shown ? 'from open' : rangeLabel}
                </Text>
              </View>
            </>
          )}
        </View>
        <SegmentedControl segments={MODE_SEGMENTS} value={mode} onChange={changeMode} style={{ width: 150 }} />
      </View>

      {/* Candle OHLC at the scrubbed bar. Full width and fixed height so scrubbing never shifts the chart. */}
      {mode === 'candle' && (
        <View style={{ flexDirection: 'row', height: OHLC_ROW_HEIGHT, marginTop: spacing.xs }}>
          {shown &&
            (['open', 'high', 'low', 'close'] as const).map((k) => (
              <Text key={k} variant="caption" color="textSecondary" numeric numberOfLines={1} style={{ flex: 1 }}>
                <Text variant="caption" color="textMuted">
                  {k[0].toUpperCase()}{' '}
                </Text>
                {formatUSD(shown[k], { maxDecimals: digits }).slice(1)}
              </Text>
            ))}
        </View>
      )}

      <View onLayout={onLayout} style={{ marginTop: spacing.sm }}>
        {renderBody()}
      </View>

      {/* Ranges */}
      <View style={{ flexDirection: 'row', marginTop: spacing.md }}>
        {CHART_RANGES.map((r) => {
          const selected = r.days === days;
          return (
            <PressableScale
              key={r.days}
              onPress={() => setDays(r.days)}
              accessibilityRole="tab"
              accessibilityState={{ selected }}
              style={{
                flex: 1,
                height: 32,
                alignItems: 'center',
                justifyContent: 'center',
                borderRadius: radius.sm,
                backgroundColor: selected ? colors.accentMuted : 'transparent',
              }}
            >
              <Text variant="bodySm" weight="600" style={{ color: selected ? colors.accent : colors.textMuted }}>
                {r.label}
              </Text>
            </PressableScale>
          );
        })}
      </View>

      {/* Range stats */}
      {chart && (
        <View
          style={{
            flexDirection: 'row',
            marginTop: spacing.md,
            paddingTop: spacing.md,
            borderTopWidth: 1,
            borderTopColor: colors.hairline,
          }}
        >
          {[
            { label: 'High', value: chart.high },
            { label: 'Low', value: chart.low },
            { label: 'Open', value: open },
          ].map((s) => (
            <View key={s.label} style={{ flex: 1 }}>
              <Text variant="caption" color="textMuted">
                {s.label}
              </Text>
              <Text variant="bodySm" weight="600" numeric>
                {formatPrice(s.value)}
              </Text>
            </View>
          ))}
        </View>
      )}
    </View>
  );
}
