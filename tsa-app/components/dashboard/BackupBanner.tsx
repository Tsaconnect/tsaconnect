// components/dashboard/BackupBanner.tsx
import React, { useState, useEffect } from 'react';
import { View, Text, Pressable, Alert, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';

export const BackupBanner: React.FC = () => {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
        if (backedUp === 'true') return;
        const dismissed = await AsyncStorage.getItem('backupBannerDismissed');
        if (dismissed === 'true') return;
        const remindAt = await AsyncStorage.getItem('backupBannerRemindAt');
        if (remindAt && Date.now() < parseInt(remindAt, 10)) return;
        setVisible(true);
      } catch {}
    })();
  }, []);

  if (!visible) return null;

  const handleRemindTomorrow = async () => {
    setVisible(false);
    await AsyncStorage.setItem('backupBannerRemindAt', (Date.now() + 86400000).toString());
  };

  const handleNever = async () => {
    setVisible(false);
    await AsyncStorage.setItem('backupBannerDismissed', 'true');
  };

  return (
    <View style={styles.banner}>
      <Pressable style={styles.content} onPress={() => router.push('/wallet/seedphrase')}>
        <Icon name="shield" size={20} color="#92400E" />
        <Text style={styles.text}>Back up your seed phrase to protect your funds</Text>
      </Pressable>
      <Pressable
        onPress={() =>
          Alert.alert('Dismiss Reminder', 'When would you like to be reminded?', [
            { text: 'Remind Tomorrow', onPress: handleRemindTomorrow },
            { text: 'Never', style: 'destructive', onPress: handleNever },
            { text: 'Cancel', style: 'cancel' },
          ])
        }
        style={styles.close}
        hitSlop={8}
      >
        <Icon name="close" size={18} color="#92400E" />
      </Pressable>
    </View>
  );
};

const styles = StyleSheet.create({
  banner: {
    flexDirection: 'row',
    alignItems: 'center',
    marginHorizontal: 16,
    marginTop: 8,
    padding: 12,
    backgroundColor: '#FFFBEB',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#FDE68A',
  },
  content: { flex: 1, flexDirection: 'row', alignItems: 'center', gap: 8 },
  text: { flex: 1, fontSize: 13, color: '#92400E', fontWeight: '500' },
  close: { marginLeft: 8 },
});

export default BackupBanner;
