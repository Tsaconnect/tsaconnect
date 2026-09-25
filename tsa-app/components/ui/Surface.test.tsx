jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#FFFFFF' }),
    radius: new Proxy({}, { get: () => 12 }),
    spacing: new Proxy({}, { get: () => 8 }),
    isDark: false,
  }),
}));

// eslint-disable-next-line import/first -- jest hoists the mock above the imports it replaces.
import React from 'react';
// eslint-disable-next-line import/first
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
// eslint-disable-next-line import/first
import { StyleSheet, type StyleProp, type ViewStyle } from 'react-native';
// eslint-disable-next-line import/first
import { isTranslucentColor, Surface } from './Surface';

describe('isTranslucentColor', () => {
  it('reads alpha from the forms the theme and callers use', () => {
    expect(isTranslucentColor('#EF44441F')).toBe(true);
    expect(isTranslucentColor('#F8717126')).toBe(true);
    expect(isTranslucentColor('#F00A')).toBe(true);
    expect(isTranslucentColor('rgba(239, 68, 68, 0.12)')).toBe(true);
    expect(isTranslucentColor('hsla(0, 84%, 60%, 50%)')).toBe(true);
    expect(isTranslucentColor('transparent')).toBe(true);
  });

  it('treats opaque and unknown values as opaque', () => {
    expect(isTranslucentColor('#FFFFFF')).toBe(false);
    expect(isTranslucentColor('#FFFFFFFF')).toBe(false);
    expect(isTranslucentColor('#FFFF')).toBe(false);
    expect(isTranslucentColor('rgba(0,0,0,1)')).toBe(false);
    expect(isTranslucentColor('red')).toBe(false);
    expect(isTranslucentColor(undefined)).toBe(false);
  });
});

describe('Surface elevation', () => {
  const rootStyle = (el: React.ReactElement) => {
    let tree!: ReactTestRenderer;
    act(() => { tree = TestRenderer.create(el); });
    return StyleSheet.flatten((tree.toJSON() as { props: { style: StyleProp<ViewStyle> } }).props.style);
  };

  it('keeps its shadow on an opaque background', () => {
    expect(rootStyle(<Surface elevation="sm" />).elevation).toBe(1);
  });

  it('drops its shadow on a tinted background, which Android would draw through the fill', () => {
    const style = rootStyle(<Surface elevation="sm" style={{ backgroundColor: '#EF44441F' }} />);
    expect(style.elevation).toBeUndefined();
    expect(style.shadowOpacity).toBeUndefined();
    expect(style.backgroundColor).toBe('#EF44441F');
  });
});
