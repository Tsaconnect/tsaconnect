// Convenience re-export so consumers can `import { useTheme } from '@/theme'`
// or '@/theme/useTheme'. Implementation lives in ThemeProvider to avoid a cycle.
export { useTheme, useThemeControls } from './ThemeProvider';
export type { Theme } from './ThemeProvider';
