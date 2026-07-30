// app/profile/index.tsx
//
// The STANDALONE profile route. Unlike app/(dashboard)/profile.tsx — which
// renders the same screen as a drawer destination and inherits the drawer's
// native "Profile" header — this one sits outside the drawer and its layout
// registers `index` with headerShown:false, so nothing draws a back control.
// The drawer avatar and Settings both push here, and both were dead ends.
//
// The header lives here rather than inside ProfileScreen precisely because the
// two routes need different chrome; putting it in the shared component stacked
// two nav bars on the drawer route.
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { ScreenHeader } from '@/components/ui';
import { useTheme } from '@/theme';
import ProfileScreen from '../../components/profile/profile';

const Profile = () => {
  const { colors } = useTheme();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Profile" />
      <View style={{ flex: 1 }}>
        <ProfileScreen />
      </View>
    </SafeAreaView>
  );
};

export default Profile;
