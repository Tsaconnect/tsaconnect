// Inter font loading for the TSA Signature system. JS-only (@expo-google-fonts/inter
// ships the TTFs + a font map; loaded at runtime via expo-font — no native rebuild).
import { useFonts } from 'expo-font';
import {
  Inter_400Regular,
  Inter_500Medium,
  Inter_600SemiBold,
  Inter_700Bold,
  Inter_800ExtraBold,
} from '@expo-google-fonts/inter';

/**
 * Loads the Inter family used by the design system. Returns [loaded, error].
 * Call once in the root layout and hold the splash until `loaded` is true.
 */
export function useAppFonts(): [boolean, Error | null] {
  const [loaded, error] = useFonts({
    Inter_400Regular,
    Inter_500Medium,
    Inter_600SemiBold,
    Inter_700Bold,
    Inter_800ExtraBold,
  });
  return [loaded, error ?? null];
}
