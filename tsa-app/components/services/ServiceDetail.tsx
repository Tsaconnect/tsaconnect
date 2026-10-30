// components/services/ServiceDetail.tsx
//
// Service detail content card — the hero image, title/address, the locked vs.
// revealed contact-details state, and the optional "See Catalogue" link.
// Restyled onto the TSA Signature design system (@/theme + @/components/ui);
// the props interface, exports, every callback wiring (onRevealPress,
// onCopyPress per contact field, onCataloguePress), the loading/disabled
// gating, and the contactPaid + contact presence gating are preserved verbatim.
import React from 'react';
import { View, Image } from 'react-native';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Icon,
  PressableScale,
  Text,
  type IconName,
} from '@/components/ui';
import type { ContactInfo } from '../../services/serviceContactApi';

interface ServiceDetailCardProps {
  title: string;
  address: string;
  imageSrc: string;
  contactPaid: boolean;
  contact?: ContactInfo | null;
  feeDisplay?: string;
  /** One line on what the provider charges beyond the contact fee, if known. */
  providerFeeNote?: string;
  loading?: boolean;
  onRevealPress?: () => void;
  onCopyPress?: (value: string) => void;
  /**
   * If provided, renders the "See Catalogue" link and triggers it on tap.
   * Pass undefined to hide the link entirely (e.g. when the merchant only
   * uploaded the hero image and there's no additional gallery).
   */
  onCataloguePress?: () => void;
  /**
   * Opens the service pricing sheet (price + call-out / booking fare / menu /
   * slots → escrow checkout). For contact-type services this is gated behind the
   * one-time contact fee (actionable only once `contactPaid`). For order/booking
   * types it is available immediately — those flows don't require the fee.
   */
  onPricingPress?: () => void;
  /**
   * The category's service type. 'contact' (default) keeps the fee-gated
   * behavior; 'order'/'booking' ungate the pricing flow (no contact fee needed).
   */
  serviceType?: 'contact' | 'order' | 'booking';
  /**
   * Whether `serviceType` has been resolved from the product yet. It starts
   * unresolved (default 'contact'), so the contact-fee block is withheld until
   * this is true — otherwise an order/booking listing flashes the fee block
   * before its type is known. Defaults to true for callers that resolve
   * synchronously.
   */
  serviceTypeResolved?: boolean;
}

const ServiceDetailCard = ({
  title,
  address,
  imageSrc,
  contactPaid,
  contact,
  feeDisplay = '$0.10',
  providerFeeNote,
  loading,
  onRevealPress,
  onCopyPress,
  onCataloguePress,
  onPricingPress,
  serviceType = 'contact',
  serviceTypeResolved = true,
}: ServiceDetailCardProps) => {
  const { colors, spacing, radius } = useTheme();
  // Order/booking services are not contact-gated — their pricing/booking flow is
  // reachable without paying the $0.10 contact reveal. Only contact services gate.
  const pricingUnlocked = serviceType !== 'contact' || contactPaid;
  // Only contact services have a pay-to-unlock contact fee. Withhold the block
  // until the type is resolved so order/booking never flash the fee.
  const showContactBlock = serviceTypeResolved && serviceType === 'contact';

  // A single tap-to-copy contact row. Keeps the exact onCopyPress(value)
  // contract — the parent owns clipboard + toast, so this row only forwards.
  const ContactRow = ({
    icon,
    value,
    showDivider,
  }: {
    icon: IconName;
    value: string;
    showDivider: boolean;
  }) => (
    <PressableScale
      onPress={() => onCopyPress?.(value)}
      accessibilityRole="button"
      accessibilityLabel={`Copy ${value}`}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.sm,
        paddingVertical: spacing.sm,
        borderTopWidth: showDivider ? 1 : 0,
        borderTopColor: colors.hairline,
      }}
    >
      <Icon name={icon} size={16} color={colors.accent} />
      <Text variant="bodySm" color="textPrimary" style={{ flex: 1 }} numberOfLines={1}>
        {value}
      </Text>
      <Icon name="copy" size={14} color={colors.textMuted} />
    </PressableScale>
  );

  return (
    <View
      style={{
        backgroundColor: colors.surface,
        borderRadius: radius.lg,
        overflow: 'hidden',
        marginBottom: spacing.xl,
        borderWidth: 1,
        borderColor: colors.border,
      }}
    >
      <Image
        source={{ uri: imageSrc }}
        style={{ width: '100%', height: 200, resizeMode: 'contain', backgroundColor: colors.surfaceAlt }}
      />
      <View style={{ padding: spacing.lg }}>
        <Text variant="title3" weight="700">
          {title}
        </Text>
        <Text variant="bodySm" color="textMuted" style={{ marginTop: spacing.xs, marginBottom: spacing.md }}>
          {address}
        </Text>

        {/* Contact Details Section — contact services only (pay-to-unlock). */}
        {showContactBlock ? (
          contactPaid && contact ? (
          <AccentCard padding="md" style={{ marginBottom: spacing.lg }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                marginBottom: spacing.sm,
              }}
            >
              <Icon name="checkCircle" size={18} color={colors.success} />
              <Text variant="bodySm" weight="600">
                Contact Details
              </Text>
            </View>

            {(() => {
              const rows: { icon: IconName; value: string }[] = [];
              if (contact.name) rows.push({ icon: 'user', value: contact.name });
              if (contact.phone) rows.push({ icon: 'mobile', value: contact.phone });
              if (contact.email) rows.push({ icon: 'mail', value: contact.email });
              if (contact.address) rows.push({ icon: 'location', value: contact.address });
              return rows.map((row, i) => (
                <ContactRow key={row.icon} icon={row.icon} value={row.value} showDivider={i > 0} />
              ));
            })()}
          </AccentCard>
        ) : (
          <AccentCard padding="md" style={{ marginBottom: spacing.lg }}>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                marginBottom: spacing.md,
              }}
            >
              <Icon name="lock" size={20} color={colors.accent} />
              <Text variant="bodySm" color="textMuted" style={{ flex: 1 }}>
                Contact details locked
              </Text>
              <Badge label={feeDisplay} background={colors.accent} color={colors.onAccent} size="sm" />
            </View>
            {providerFeeNote ? (
              <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.sm }}>
                {providerFeeNote}
              </Text>
            ) : null}
            <Button
              title={`Reveal Contact — ${feeDisplay}`}
              onPress={onRevealPress}
              loading={loading}
              disabled={loading}
            />
          </AccentCard>
          )
        ) : null}

        {/* Pricing services — View / PayNow. Pay the service price + call-out
            (or a booking fare) through escrow. Gated behind the contact fee:
            actionable only once contactPaid, otherwise shown locked. */}
        {onPricingPress ? (
          pricingUnlocked ? (
            <Button
              title={
                serviceType === 'order'
                  ? 'View menu & order'
                  : serviceType === 'booking'
                    ? 'Book a slot'
                    : 'Pricing services — View / Pay Now'
              }
              variant="secondary"
              leftIcon="pricetag"
              onPress={onPricingPress}
              style={{ marginBottom: spacing.lg }}
            />
          ) : (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                paddingVertical: spacing.md,
                paddingHorizontal: spacing.md,
                borderRadius: radius.md,
                borderWidth: 1,
                borderColor: colors.border,
                backgroundColor: colors.surfaceAlt,
                marginBottom: spacing.lg,
              }}
            >
              <Icon name="lock" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textMuted" style={{ flex: 1 }}>
                Pricing & Pay Now — pay the contact fee first to unlock
              </Text>
            </View>
          )
        ) : null}

        {onCataloguePress ? (
          <View style={{ flexDirection: 'row', justifyContent: 'center', alignItems: 'center' }}>
            <PressableScale onPress={onCataloguePress} hitSlop={8}>
              <Text variant="bodySm" weight="600" color="info" align="center">
                See Catalogue
              </Text>
            </PressableScale>
          </View>
        ) : null}
      </View>
    </View>
  );
};

export default ServiceDetailCard;
