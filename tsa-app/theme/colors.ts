// Semantic color tokens for the TSA Signature system. Two implementations of the
// SAME ColorTokens interface — the interface guarantees light/dark key parity
// (also asserted in a jest test). Components read these via useTheme(), never palette.
import { palette as p } from './tokens';

export interface ColorTokens {
  // Surfaces
  background: string;
  backgroundAlt: string;
  surface: string;
  surfaceAlt: string;
  surfaceElevated: string;
  surfaceDisabled: string;
  // Lines / overlays
  border: string;
  borderDisabled: string;
  hairline: string;
  overlay: string; // scrim behind sheets/modals
  // Text
  textPrimary: string;
  textSecondary: string;
  textMuted: string;
  textDisabled: string;
  textInverse: string;
  // Accent (brand gold)
  accent: string;
  accentHover: string;
  accentMuted: string; // tinted background
  accentDisabled: string;
  onAccent: string; // text/icon on accent fill
  accentRail: string; // signature card rail
  focusRing: string;
  // Semantic
  success: string;
  successBg: string;
  warning: string;
  warningBg: string;
  danger: string;
  dangerBg: string;
  info: string;
  infoBg: string;
  escrow: string; // funds-frozen (locked/releasing/disputed)
  escrowBg: string;
  // Trade sides + presence
  buy: string;
  sell: string;
  online: string;
}

export const lightColors: ColorTokens = {
  background: p.n50,
  backgroundAlt: p.n100,
  surface: p.white,
  surfaceAlt: p.n100,
  surfaceElevated: p.white,
  surfaceDisabled: p.n100,
  border: p.n200,
  borderDisabled: p.n150,
  hairline: p.n150,
  overlay: '#16201F99', // slate @ 60%
  textPrimary: p.slate900,
  textSecondary: p.n600,
  textMuted: p.n400,
  textDisabled: p.n300,
  textInverse: p.white,
  accent: p.gold500,
  accentHover: p.gold600,
  accentMuted: '#C8961E1F', // gold @ ~12%
  accentDisabled: '#C8961E66',
  onAccent: p.white,
  accentRail: p.gold400,
  focusRing: '#C8961E66', // gold @ 40%
  success: p.success,
  successBg: '#10B9811F',
  warning: p.warning,
  warningBg: '#F59E0B1F',
  danger: p.danger,
  dangerBg: '#EF44441F',
  info: p.info,
  infoBg: '#3B82F61F',
  escrow: p.escrow,
  escrowBg: '#7C6FB01F',
  buy: p.buy,
  sell: p.sell,
  online: p.success,
};

export const darkColors: ColorTokens = {
  background: p.slate950,
  backgroundAlt: p.slate900,
  surface: p.slate900,
  surfaceAlt: p.n800,
  surfaceElevated: p.slateElevated,
  surfaceDisabled: p.n800,
  border: '#3D4A49',
  borderDisabled: '#26302F',
  hairline: '#26302F',
  overlay: '#00000099',
  textPrimary: '#F2F4F3',
  textSecondary: '#9FB0AE',
  textMuted: '#6E807E',
  textDisabled: '#4A5856',
  textInverse: p.slate950,
  accent: p.goldBright,
  accentHover: p.gold300,
  accentMuted: '#E5C04B26', // bright gold @ ~15%
  accentDisabled: '#E5C04B59',
  onAccent: p.slate950,
  accentRail: p.goldBright,
  focusRing: '#E5C04B73',
  success: p.successBright,
  successBg: '#34D39926',
  warning: p.warningBright,
  warningBg: '#FBBF2426',
  danger: p.dangerBright,
  dangerBg: '#F8717126',
  info: p.infoBright,
  infoBg: '#60A5FA26',
  escrow: p.escrowBright,
  escrowBg: '#A99BE026',
  buy: p.successBright,
  sell: '#F87166',
  online: p.successBright,
};
