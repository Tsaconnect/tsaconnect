// Simple inline EmptyState component
import { View, Text, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';

// Update the EmptyState component interface and render method
interface EmptyStateProps {
  title: string;
  message?: string;
  icon?: string;
  action?: React.ReactNode; // Add this line
}

export const EmptyState = ({
  title,
  message = 'Try adjusting your search or filter to find what you\'re looking for.',
  icon = 'storefront-outline',
  action // Add this line
}: EmptyStateProps) => {
  return (
    <View style={emptyStyles.container}>
      <View style={emptyStyles.iconContainer}>
        <Ionicons name={icon as any} size={64} color="#D4AF37" />
      </View>
      <Text style={emptyStyles.title}>{title}</Text>
      <Text style={emptyStyles.message}>{message}</Text>
      {/* Add this section */}
      {action && <View style={emptyStyles.actionContainer}>{action}</View>}
    </View>
  );
};

const emptyStyles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 40,
    paddingVertical: 60,
  },
  iconContainer: {
    width: 120,
    height: 120,
    borderRadius: 60,
    backgroundColor: 'rgba(212, 175, 55, 0.1)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 24,
  },
  title: {
    fontSize: 24,
    fontWeight: '600',
    color: '#1A1A1A',
    marginBottom: 12,
    textAlign: 'center',
  },
  message: {
    fontSize: 16,
    color: '#666',
    textAlign: 'center',
    lineHeight: 24,
  },
    actionContainer: {
    marginTop: 16,
  },
});