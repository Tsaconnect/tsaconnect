// components/services/ServiceCard.tsx
//
// Service card — shows a service's image, title, and description with a
// "Request Service" CTA that navigates to the service detail screen. Migrated
// onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). The props interface, exports, the navigation route + params
// (`/servicedetail` with { id, title, image, description }), and the hasImage
// gating are all unchanged — this is a visual / structural restyle only.
import React from 'react';
import { Image, View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Card, Icon, PressableScale, Text } from '@/components/ui';

interface ServiceCardProps {
  id: string;
  title: string;
  image?: string;
  description?: string;
}

const ServiceCard: React.FC<ServiceCardProps> = ({ id, title, image, description }) => {
  const { colors, spacing, radius } = useTheme();
  const hasImage = typeof image === 'string' && image.length > 0;

  const selectService = () => {
    router.push({
      pathname: '/servicedetail',
      params: { id, title, image: image ?? '', description: description ?? '' },
    });
  };

  return (
    <PressableScale onPress={selectService} style={{ marginHorizontal: spacing.lg, marginVertical: spacing.sm }}>
      <Card padding="lg">
        <View
          style={{
            width: '100%',
            height: 180,
            borderRadius: radius.md,
            overflow: 'hidden',
            backgroundColor: colors.surfaceAlt,
            marginBottom: spacing.md,
          }}
        >
          {hasImage ? (
            <Image source={{ uri: image }} resizeMode="cover" style={{ width: '100%', height: '100%' }} />
          ) : (
            <View
              style={{
                flex: 1,
                alignItems: 'center',
                justifyContent: 'center',
                backgroundColor: colors.accentMuted,
              }}
            >
              <Icon name="storefront" size={48} color={colors.accent} />
            </View>
          )}
        </View>

        <Text variant="title3" weight="700" numberOfLines={2}>
          {title}
        </Text>
        {description ? (
          <Text
            variant="bodySm"
            color="textSecondary"
            numberOfLines={3}
            style={{ marginTop: spacing.xs }}
          >
            {description}
          </Text>
        ) : null}
        <Button title="Request Service" onPress={selectService} style={{ marginTop: spacing.md }} />
      </Card>
    </PressableScale>
  );
};

export default ServiceCard;
