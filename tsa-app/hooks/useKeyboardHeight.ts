// hooks/useKeyboardHeight.ts
//
// Tracks the on-screen keyboard height so a scroll view can pad its bottom by
// that amount, letting every field scroll clear of the keypad.
//
// Needed because Android runs softwareKeyboardLayoutMode "pan" (app.json): the
// window is panned, not resized, so KeyboardAvoidingView alone doesn't lift the
// fields BELOW the focused one — they stay hidden under the keyboard. Adding the
// keyboard height to the ScrollView's paddingBottom gives enough scrollable room
// to bring any field above the keypad.
import { useEffect, useState } from 'react';
import { Keyboard, Platform } from 'react-native';

export function useKeyboardHeight(): number {
  const [height, setHeight] = useState(0);

  useEffect(() => {
    // iOS emits Will* early (smoother); Android only reliably emits Did*.
    const showEvt = Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow';
    const hideEvt = Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide';
    const show = Keyboard.addListener(showEvt, (e) => setHeight(e.endCoordinates?.height ?? 0));
    const hide = Keyboard.addListener(hideEvt, () => setHeight(0));
    return () => {
      show.remove();
      hide.remove();
    };
  }, []);

  return height;
}
