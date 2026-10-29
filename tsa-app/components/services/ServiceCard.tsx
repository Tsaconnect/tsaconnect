// components/services/ServiceCard.tsx
//
// Service card — one compact row per vendor: thumbnail, type badge, title, a
// two-line description and the location, so a category list shows several
// vendors per screen. The whole row opens the service detail screen
// (`/servicedetail` with { id, title, image, description }).
import React from 'react';
import { Image, View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Badge, Card, Icon, PressableScale, Text } from '@/components/ui';

type ServiceType = 'contact' | 'order' | 'booking';

// Human-facing label for the subtle type badge shown on the card.
const SERVICE_TYPE_LABEL: Record<ServiceType, string> = {
  contact: 'Contact',
  order: 'Ordering',
  booking: 'Booking',
};

const THUMB_SIZE = 88;

interface ServiceCardProps {
  id: string;
  title: string;
  image?: string;
  description?: string;
  location?: string;
  serviceType?: ServiceType;
}

const ServiceCard: React.FC<ServiceCardProps> = ({ id, title, image, description, location, serviceType }) => {
  const { colors, spacing, radius } = useTheme();
  const hasImage = typeof image === 'string' && image.length > 0;

  const selectService = () => {
    router.push({
      pathname: '/servicedetail',
      params: { id, title, image: image ?? '', description: description ?? '' },
    });
  };

  return (
    <PressableScale
      onPress={selectService}
      accessibilityRole="button"
      accessibilityLabel={`Open ${title}`}
      style={{ marginHorizontal: spacing.lg, marginVertical: spacing.xs }}
    >
      <Card padding="md">
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <View
            style={{
              width: THUMB_SIZE,
              height: THUMB_SIZE,
              borderRadius: radius.md,
              overflow: 'hidden',
              backgroundColor: colors.accentMuted,
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            {hasImage ? (
              <Image source={{ uri: image }} resizeMode="cover" style={{ width: '100%', height: '100%' }} />
            ) : (
              <Icon name="storefront" size={32} color={colors.accent} />
            )}
          </View>

          <View style={{ flex: 1, gap: 2 }}>
            {serviceType ? (
              <Badge
                label={SERVICE_TYPE_LABEL[serviceType]}
                size="sm"
                color={colors.accent}
                background={colors.accentMuted}
                style={{ alignSelf: 'flex-start', marginBottom: 2 }}
              />
            ) : null}
            <Text variant="bodyMed" weight="700" numberOfLines={1}>
              {title}
            </Text>
            {description ? (
              <Text variant="bodySm" color="textSecondary" numberOfLines={2}>
                {description}
              </Text>
            ) : null}
            {location ? (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: 4, marginTop: 2 }}>
                <Icon name="location" size={12} color={colors.textMuted} />
                <Text variant="caption" color="textMuted" numberOfLines={1} style={{ flex: 1 }}>
                  {location}
                </Text>
              </View>
            ) : null}
          </View>

          <Icon name="chevronRight" size={18} color={colors.textMuted} />
        </View>
      </Card>
    </PressableScale>
  );
};

export default ServiceCard;
