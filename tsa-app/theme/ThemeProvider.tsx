// TSA Signature theme context. The active scheme follows a user preference
// (light / dark / system) that DEFAULTS TO LIGHT and is persisted across
// launches; 'system' defers to the OS color scheme. An in-app toggle drives
// setScheme(). Mount once at the app root, outermost.
import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { StatusBar, useColorScheme } from 'react-native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { ColorTokens, darkColors, lightColors } from './colors';
import {
  fontFamily,
  gradients,
  motion,
  radius,
  spacing,
  typography,
  z,
} from './tokens';

export type ColorScheme = 'light' | 'dark';
export type SchemePreference = ColorScheme | 'system';

export interface Theme {
  scheme: ColorScheme;
  isDark: boolean;
  colors: ColorTokens;
  spacing: typeof spacing;
  radius: typeof radius;
  type: typeof typography;
  fonts: typeof fontFamily;
  motion: typeof motion;
  gradients: typeof gradients;
  z: typeof z;
}

function buildTheme(scheme: ColorScheme): Theme {
  return {
    scheme,
    isDark: scheme === 'dark',
    colors: scheme === 'dark' ? darkColors : lightColors,
    spacing,
    radius,
    type: typography,
    fonts: fontFamily,
    motion,
    gradients,
    z,
  };
}

interface ThemeContextValue {
  theme: Theme;
  preference: SchemePreference;
  setScheme: (pref: SchemePreference) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

// Persisted preference key. Defaults to 'light' until a stored value loads.
const PREFERENCE_KEY = 'tsa.theme.preference';

export const ThemeProvider: React.FC<{
  children: React.ReactNode;
  /** Force a scheme (tests / gallery). Overrides system + preference. */
  forceScheme?: ColorScheme;
}> = ({ children, forceScheme }) => {
  const system = useColorScheme();
  // Default to LIGHT (not system) — the app opens light unless the user opts in.
  const [preference, setPreference] = useState<SchemePreference>('light');

  // Hydrate the persisted preference on mount.
  useEffect(() => {
    let active = true;
    AsyncStorage.getItem(PREFERENCE_KEY)
      .then((stored) => {
        if (active && (stored === 'light' || stored === 'dark' || stored === 'system')) {
          setPreference(stored);
        }
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  const resolved: ColorScheme = forceScheme
    ? forceScheme
    : preference === 'system'
      ? system === 'dark'
        ? 'dark'
        : 'light'
      : preference;

  const theme = useMemo(() => buildTheme(resolved), [resolved]);
  const setScheme = useCallback((pref: SchemePreference) => {
    setPreference(pref);
    AsyncStorage.setItem(PREFERENCE_KEY, pref).catch(() => {});
  }, []);

  const value = useMemo(
    () => ({ theme, preference, setScheme }),
    [theme, preference, setScheme]
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
};

/** Access the active theme. Throws if used outside ThemeProvider. */
export function useTheme(): Theme {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used within a ThemeProvider');
  return ctx.theme;
}

/** Access scheme preference + setter (for an in-app light/dark toggle). */
export function useThemeControls(): Pick<ThemeContextValue, 'preference' | 'setScheme'> {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useThemeControls must be used within a ThemeProvider');
  return { preference: ctx.preference, setScheme: ctx.setScheme };
}

/** Status bar that follows the active theme (replaces the hardcoded light bar). */
export const ThemedStatusBar: React.FC = () => {
  const { isDark, colors } = useTheme();
  return (
    <StatusBar
      barStyle={isDark ? 'light-content' : 'dark-content'}
      backgroundColor={colors.background}
    />
  );
};
