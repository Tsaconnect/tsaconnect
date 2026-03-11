// Simple inline LoadingState component
import { View, ActivityIndicator, StyleSheet } from 'react-native';

export const LoadingState = () => {
  return (
    <View style={loadingStyles.container}>
      <ActivityIndicator size="large" color="#D4AF37" />
    </View>
  );
};

const loadingStyles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 40,
  },
});