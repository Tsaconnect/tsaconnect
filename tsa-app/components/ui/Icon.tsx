// Semantic icon registry — screens reference names like "verified"/"copy", never
// raw glyphs, so iconography stays consistent and swappable in one place.
import React from 'react';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { useTheme } from '@/theme';

type IconEntry =
  | { set: 'ion'; glyph: React.ComponentProps<typeof Ionicons>['name'] }
  | { set: 'mci'; glyph: React.ComponentProps<typeof MaterialCommunityIcons>['name'] };

const REGISTRY = {
  verified: { set: 'ion', glyph: 'shield-checkmark' },
  trusted: { set: 'mci', glyph: 'shield-star' },
  bolt: { set: 'ion', glyph: 'flash' },
  star: { set: 'ion', glyph: 'star' },
  starOutline: { set: 'ion', glyph: 'star-outline' },
  thumbUp: { set: 'ion', glyph: 'thumbs-up' },
  thumbDown: { set: 'ion', glyph: 'thumbs-down' },
  bank: { set: 'mci', glyph: 'bank' },
  card: { set: 'ion', glyph: 'card' },
  mobile: { set: 'ion', glyph: 'phone-portrait' },
  wallet: { set: 'ion', glyph: 'wallet' },
  swap: { set: 'mci', glyph: 'swap-horizontal' },
  copy: { set: 'ion', glyph: 'copy-outline' },
  check: { set: 'ion', glyph: 'checkmark' },
  checkCircle: { set: 'ion', glyph: 'checkmark-circle' },
  close: { set: 'ion', glyph: 'close' },
  closeCircle: { set: 'ion', glyph: 'close-circle' },
  chevronRight: { set: 'ion', glyph: 'chevron-forward' },
  chevronLeft: { set: 'ion', glyph: 'chevron-back' },
  chevronDown: { set: 'ion', glyph: 'chevron-down' },
  arrowRight: { set: 'ion', glyph: 'arrow-forward' },
  clock: { set: 'ion', glyph: 'time-outline' },
  shield: { set: 'ion', glyph: 'shield-outline' },
  lock: { set: 'ion', glyph: 'lock-closed' },
  dispute: { set: 'ion', glyph: 'alert-circle' },
  warning: { set: 'ion', glyph: 'warning' },
  info: { set: 'ion', glyph: 'information-circle' },
  upload: { set: 'ion', glyph: 'cloud-upload-outline' },
  camera: { set: 'ion', glyph: 'camera' },
  image: { set: 'ion', glyph: 'image' },
  search: { set: 'ion', glyph: 'search' },
  filter: { set: 'ion', glyph: 'options-outline' },
  plus: { set: 'ion', glyph: 'add' },
  refresh: { set: 'ion', glyph: 'refresh' },
  user: { set: 'ion', glyph: 'person' },
  dots: { set: 'ion', glyph: 'ellipsis-horizontal' },
  send: { set: 'ion', glyph: 'send' },
  trash: { set: 'ion', glyph: 'trash-outline' },
  pause: { set: 'ion', glyph: 'pause' },
  play: { set: 'ion', glyph: 'play' },
  empty: { set: 'mci', glyph: 'inbox-outline' },
  swapVertical: { set: 'ion', glyph: 'swap-vertical' },
  list: { set: 'ion', glyph: 'list' },
  storefront: { set: 'ion', glyph: 'storefront' },
  trendingUp: { set: 'ion', glyph: 'trending-up' },
  trendingDown: { set: 'ion', glyph: 'trending-down' },
  pool: { set: 'mci', glyph: 'safe' },
  // Added for the app-wide migration (dashboard / wallet / marketplace / notifications).
  eye: { set: 'ion', glyph: 'eye' },
  eyeOff: { set: 'ion', glyph: 'eye-off' },
  arrowUp: { set: 'ion', glyph: 'arrow-up' },
  arrowDown: { set: 'ion', glyph: 'arrow-down' },
  notifications: { set: 'ion', glyph: 'notifications' },
  cart: { set: 'ion', glyph: 'cart' },
  pricetag: { set: 'ion', glyph: 'pricetag' },
  localOffer: { set: 'ion', glyph: 'pricetags' },
  nearMe: { set: 'ion', glyph: 'navigate' },
  construction: { set: 'ion', glyph: 'construct' },
} as const satisfies Record<string, IconEntry>;

export type IconName = keyof typeof REGISTRY;

export interface IconProps {
  name: IconName;
  size?: number;
  color?: string;
}

export const Icon: React.FC<IconProps> = ({ name, size = 20, color }) => {
  const { colors } = useTheme();
  const entry = REGISTRY[name];
  const c = color ?? colors.textPrimary;
  // Defense-in-depth: an unknown name (e.g. a stale `as IconName` cast) renders
  // nothing rather than crashing the screen.
  if (!entry) return null;
  if (entry.set === 'mci') {
    return <MaterialCommunityIcons name={entry.glyph} size={size} color={c} />;
  }
  return <Ionicons name={entry.glyph} size={size} color={c} />;
};
