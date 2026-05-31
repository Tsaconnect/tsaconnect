// TSA Signature design tokens — raw scales (palette, spacing, radius, type, motion).
// Single source of truth for the P2P design system. Semantic light/dark colors are
// derived in ./colors.ts; do not reference palette directly from components.

export const palette = {
  // Brand gold ramp (the signature accent)
  gold50: '#FBF4E0',
  gold100: '#F6E7BC',
  gold200: '#EFD488',
  gold300: '#E6BF53',
  gold400: '#D9A92F',
  gold500: '#C8961E', // light-mode accent (AA on white for bold/large text)
  gold600: '#A87B16',
  gold700: '#856014',
  goldBright: '#E5C04B', // dark-mode accent (brighter, pops on slate)

  // Warm-brown heritage
  brown400: '#B5895A',
  brown500: '#9D6B38',
  brown600: '#7E5429',
  brown700: '#5E3E1E',

  // Neutral / slate ramp
  white: '#FFFFFF',
  n50: '#FAFAF8',
  n100: '#F2F3F1',
  n150: '#E8EAE7',
  n200: '#DCDFDB',
  n300: '#C3C8C3',
  n400: '#9AA3A0',
  n500: '#737E7B',
  n600: '#5B6B6A',
  n700: '#3D4A49',
  n800: '#26302F',
  slate900: '#16201F',
  slate950: '#10181A',
  slateElevated: '#1E2A29',
  black: '#000000',

  // Semantic hues (light / dark variants)
  success: '#10B981',
  successBright: '#34D399',
  warning: '#F59E0B',
  warningBright: '#FBBF24',
  danger: '#EF4444',
  dangerBright: '#F87171',
  info: '#3B82F6',
  infoBright: '#60A5FA',

  // Trade-side + escrow state
  buy: '#10B981',
  sell: '#F0584B',
  escrow: '#7C6FB0', // funds-frozen neutral-violet (locked/releasing/disputed)
  escrowBright: '#A99BE0',
} as const;

export const spacing = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 16,
  xl: 20,
  '2xl': 24,
  '3xl': 32,
  '4xl': 40,
} as const;
export type SpacingKey = keyof typeof spacing;

export const radius = {
  sm: 8,
  md: 12,
  lg: 16, // cards
  xl: 20,
  pill: 999,
} as const;
export type RadiusKey = keyof typeof radius;

export type FontWeightToken = '400' | '500' | '600' | '700' | '800';

// RN cannot synthesize weights from one face — map each weight to its Inter family.
export const fontFamily: Record<FontWeightToken, string> = {
  '400': 'Inter_400Regular',
  '500': 'Inter_500Medium',
  '600': 'Inter_600SemiBold',
  '700': 'Inter_700Bold',
  '800': 'Inter_800ExtraBold',
};

// Weights tuned for a calmer, more professional feel: headings top out at bold
// (700) rather than extrabold, and large display text uses semibold so it reads
// as confident, not heavy. Hierarchy now leans on size + spacing + color more
// than weight. (Numbers stay a touch heavier via AmountText.)
export const typography = {
  display: { fontSize: 32, lineHeight: 40, weight: '700', letterSpacing: -0.5 },
  title1: { fontSize: 26, lineHeight: 32, weight: '700', letterSpacing: -0.3 },
  title2: { fontSize: 21, lineHeight: 28, weight: '600', letterSpacing: -0.2 },
  title3: { fontSize: 17, lineHeight: 24, weight: '600' },
  body: { fontSize: 16, lineHeight: 23, weight: '400' },
  bodyMed: { fontSize: 16, lineHeight: 23, weight: '600' },
  bodySm: { fontSize: 14, lineHeight: 20, weight: '400' },
  caption: { fontSize: 12, lineHeight: 16, weight: '500' },
  overline: { fontSize: 11, lineHeight: 14, weight: '600', letterSpacing: 0.6 },
} as const;
export type TypeVariant = keyof typeof typography;

export const motion = {
  spring: {
    press: { damping: 18, stiffness: 320, mass: 0.6 },
    enter: { damping: 20, stiffness: 200 },
  },
  timing: { fast: 120, base: 200, slow: 320 },
} as const;

// Gradient stops for the signature accent rail / hero (consumed by AccentCard etc.)
export const gradients = {
  accentRailLight: ['#E6BF53', '#9D6B38'] as [string, string],
  accentRailDark: ['#E5C04B', '#B5895A'] as [string, string],
} as const;

export const z = {
  base: 0,
  card: 1,
  header: 10,
  overlay: 100,
  sheet: 110,
  toast: 120,
} as const;
