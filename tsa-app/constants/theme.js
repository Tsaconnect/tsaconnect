import { Dimensions } from "react-native";
import { lightColors } from "../theme/colors";
const { height, width } = Dimensions.get("window");

// ─────────────────────────────────────────────────────────────────────────────
// LEGACY COLOR BRIDGE (Phase 0 of the app-wide TSA Signature migration, #34).
//
// `COLORS` is consumed by ~77 not-yet-migrated screens via StyleSheet.create.
// Rather than hand-edit each, we re-source every key from the light design
// tokens so the whole app instantly shares one palette. This is LIGHT-ONLY by
// nature (StyleSheet values are static) — a screen only becomes dark-adaptive
// once its phase migrates it to useTheme().
//
// Brand alignment (intentional, flagged in the PR): the legacy brown `primary`
// (#9D6B38) now maps to the signature gold `accent`, matching the redesigned
// P2P surface. The warm brown lives on in the accent rail gradients.
// ─────────────────────────────────────────────────────────────────────────────
export const COLORS = {
  primary: lightColors.accent,
  white: lightColors.surface,
  background: lightColors.background,
  lightButton: lightColors.accentHover,
  black: lightColors.textPrimary,
  grey: lightColors.textMuted,
  secondary: lightColors.accent,
  success: lightColors.success,
  warning: lightColors.warning,
  danger: lightColors.danger,
  dark: lightColors.textPrimary,
  gray: lightColors.textSecondary,
  lightGray: lightColors.border,
  lightPrimary: lightColors.accentMuted,
};

export const SIZES = {
  // Global SIZES
  base: 8,
  font: 14,
  radius: 30,
  padding: 8,
  padding2: 12,
  padding3: 16,
  small: 12,
  medium: 16,
  large: 18,
  extraLarge: 24,
  // FONTS Sizes
  largeTitle: 50,
  h1: 30,
  h2: 22,
  h3: 20,
  h4: 18,
  body1: 30,
  body2: 20,
  body3: 16,
  body4: 14,

  // App Dimensions
  width,
  height,
};

export const FONTS = {
  largeTitle: {
    fontFamily: "black",
    fontSize: SIZES.largeTitle,
    lineHeight: 55,
  },
  h1: { fontSize: SIZES.h1, lineHeight: 36 },
  h2: { fontSize: SIZES.h2, lineHeight: 30 },
  h3: { fontSize: SIZES.h3, lineHeight: 22 },
  h4: { fontSize: SIZES.h4, lineHeight: 20 },
  body1: { fontSize: SIZES.body1, lineHeight: 36 },
  body2: { fontSize: SIZES.body2, lineHeight: 30 },
  body3: { fontSize: SIZES.body3, lineHeight: 22 },
  body4: { fontSize: SIZES.body4, lineHeight: 20 },
  body5: {
    fontSize: 12,
    lineHeight: 16,
    fontFamily: 'System',
  },
};
export const SHADOWS = {
  light: {
    shadowColor: COLORS.gray,
    shadowOffset: {
      width: 0,
      height: 1,
    },
    shadowOpacity: 0.22,
    shadowRadius: 2.22,

    elevation: 3,
  },
  medium: {
    shadowColor: COLORS.gray,
    shadowOffset: {
      width: 0,
      height: 3,
    },

    shadowOpacity: 0.29,
    shadowRadius: 4.65,

    elevation: 7,
  },
  large: {
    shadowColor: COLORS.gray,
    shadowOffset: {
      width: 0,
      height: 5,
    },

    shadowOpacity: 0.29,
    shadowRadius: 6.65,

    elevation: 9,
  },
  small: {
    shadowColor: COLORS.gray,
    shadowOffset: {
      width: 0,
      height: 3,
    },

    shadowOpacity: 0.29,
    shadowRadius: 2.65,

    elevation: 5,
  },
  dark: {
    shadowColor: COLORS.gray,
    shadowOffset: {
      width: 0,
      height: 7,
    },
    shadowOpacity: 0.41,
    shadowRadius: 9.11,

    elevation: 14,
  },
};

const appTheme = { COLORS, SIZES, FONTS, SHADOWS };

export default appTheme;
