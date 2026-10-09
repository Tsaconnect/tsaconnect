// A yes/no Alert as a promise. Dismissing (Android back / tap outside) is "no".
import { Alert } from 'react-native';

export function confirmAsync(
  title: string,
  message: string,
  confirmText: string,
  cancelText = 'Cancel',
  destructive = false,
): Promise<boolean> {
  return new Promise((resolve) => {
    Alert.alert(
      title,
      message,
      [
        { text: cancelText, style: 'cancel', onPress: () => resolve(false) },
        { text: confirmText, style: destructive ? 'destructive' : 'default', onPress: () => resolve(true) },
      ],
      { cancelable: true, onDismiss: () => resolve(false) },
    );
  });
}
