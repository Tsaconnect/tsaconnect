import { Dimensions } from "react-native";
const { height, width } = Dimensions.get("window");

export const COLORS = {
  primary: "#9D6B38",
  white: "#FFFFFF",
  background: "#253334",
  //gray: "#ebedf0",
  lightButton: "#E8A14A",
  black: "#000000",
  grey: "#bab8b1",
  secondary: "#FF3FFF",
  //
  //primary: '#6366f1', // Modern indigo
  //secondary: '#8b5cf6', // Purple
  success: '#10b981', // Emerald
  warning: '#f59e0b', // Amber
  danger: '#ef4444', // Red
  dark: '#1f2937', // Gray-800
  gray: '#6b7280', // Gray-500
  lightGray: '#e5e7eb', // Gray-200
  lightPrimary: 'rgba(99, 102, 241, 0.1)',
  background: '#f9fafb', // Gray-50
  white: '#ffffff',
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
  font: 14,
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
