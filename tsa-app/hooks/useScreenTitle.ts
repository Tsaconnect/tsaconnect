// hooks/useScreenTitle.ts
//
// Sets the native header title from inside a screen, for titles that depend on
// screen state (the layout's static title is only a placeholder). Called once
// at the top of the component, so every early-return branch keeps the title.
import { useEffect } from 'react';
import { useNavigation } from 'expo-router';

export function useScreenTitle(title: string) {
  const navigation = useNavigation();
  useEffect(() => {
    navigation.setOptions({ title });
  }, [navigation, title]);
}
