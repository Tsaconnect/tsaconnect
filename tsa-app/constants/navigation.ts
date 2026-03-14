// Shared navigation header configuration
// Used across all Stack layouts for consistent header styling
import { Platform } from 'react-native';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';

const HEADER_TINT = '#9D6B38';

export const defaultScreenOptions: NativeStackNavigationOptions = {
  headerTitleAlign: 'center',
  headerTintColor: HEADER_TINT,
  headerTitleStyle: {
    fontSize: 17,
    fontWeight: '600',
    color: '#1f2937',
  },
  headerStyle: {
    backgroundColor: '#FFFFFF',
  },
  headerShadowVisible: false,
  headerBackTitleVisible: false,
};
