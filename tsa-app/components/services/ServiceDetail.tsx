import React from 'react';
import { View, Text, Image, StyleSheet, TouchableOpacity, ActivityIndicator } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { COLORS } from '../../constants';
import type { ContactInfo } from '../../services/serviceContactApi';

interface ServiceDetailCardProps {
  title: string;
  address: string;
  imageSrc: string;
  contactPaid: boolean;
  contact?: ContactInfo | null;
  feeDisplay?: string;
  loading?: boolean;
  onRevealPress?: () => void;
  onCopyPress?: (value: string) => void;
}

const ServiceDetailCard = ({
  title,
  address,
  imageSrc,
  contactPaid,
  contact,
  feeDisplay = '$0.10',
  loading,
  onRevealPress,
  onCopyPress,
}: ServiceDetailCardProps) => {
  return (
    <View style={styles.card}>
      <Image source={{ uri: imageSrc }} style={styles.image} />
      <View style={styles.detailsContainer}>
        <Text style={styles.title}>{title}</Text>
        <Text style={styles.address}>{address}</Text>

        {/* Contact Details Section */}
        {contactPaid && contact ? (
          <View style={styles.contactCard}>
            <View style={styles.contactHeader}>
              <Ionicons name="checkmark-circle" size={18} color={COLORS.success} />
              <Text style={styles.contactHeaderText}>Contact Details</Text>
            </View>

            {contact.name ? (
              <TouchableOpacity
                style={styles.contactRow}
                onPress={() => onCopyPress?.(contact.name!)}
              >
                <Ionicons name="person-outline" size={16} color={COLORS.primary} />
                <Text style={styles.contactText}>{contact.name}</Text>
                <Ionicons name="copy-outline" size={14} color={COLORS.gray} />
              </TouchableOpacity>
            ) : null}

            {contact.phone ? (
              <TouchableOpacity
                style={styles.contactRow}
                onPress={() => onCopyPress?.(contact.phone!)}
              >
                <Ionicons name="call-outline" size={16} color={COLORS.primary} />
                <Text style={styles.contactText}>{contact.phone}</Text>
                <Ionicons name="copy-outline" size={14} color={COLORS.gray} />
              </TouchableOpacity>
            ) : null}

            {contact.email ? (
              <TouchableOpacity
                style={styles.contactRow}
                onPress={() => onCopyPress?.(contact.email!)}
              >
                <Ionicons name="mail-outline" size={16} color={COLORS.primary} />
                <Text style={styles.contactText}>{contact.email}</Text>
                <Ionicons name="copy-outline" size={14} color={COLORS.gray} />
              </TouchableOpacity>
            ) : null}

            {contact.address ? (
              <TouchableOpacity
                style={styles.contactRow}
                onPress={() => onCopyPress?.(contact.address!)}
              >
                <Ionicons name="location-outline" size={16} color={COLORS.primary} />
                <Text style={styles.contactText}>{contact.address}</Text>
                <Ionicons name="copy-outline" size={14} color={COLORS.gray} />
              </TouchableOpacity>
            ) : null}
          </View>
        ) : (
          <View style={styles.lockedCard}>
            <View style={styles.lockedContent}>
              <Ionicons name="lock-closed" size={20} color={COLORS.primary} />
              <Text style={styles.lockedText}>Contact details locked</Text>
              <View style={styles.feeBadge}>
                <Text style={styles.feeBadgeText}>{feeDisplay}</Text>
              </View>
            </View>
            <TouchableOpacity
              style={styles.revealButton}
              onPress={onRevealPress}
              disabled={loading}
            >
              {loading ? (
                <ActivityIndicator color="#fff" size="small" />
              ) : (
                <Text style={styles.revealButtonText}>Reveal Contact — {feeDisplay}</Text>
              )}
            </TouchableOpacity>
          </View>
        )}

        <View style={styles.payCatalogue}>
          <TouchableOpacity>
            <Text style={styles.catalogue}>See Catalogue</Text>
          </TouchableOpacity>
        </View>
      </View>
    </View>
  );
};

export default ServiceDetailCard;

const styles = StyleSheet.create({
  card: {
    backgroundColor: '#fff',
    borderRadius: 10,
    overflow: 'hidden',
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 5,
    elevation: 3,
  },
  image: {
    width: '100%',
    height: 200,
    resizeMode: 'contain',
  },
  detailsContainer: {
    padding: 15,
  },
  title: {
    fontSize: 18,
    fontWeight: 'bold',
    marginBottom: 5,
    color: COLORS.primary,
  },
  address: {
    fontSize: 14,
    color: '#777',
    marginBottom: 10,
  },
  // Contact details (paid state)
  contactCard: {
    backgroundColor: '#f9f6f2',
    borderRadius: 10,
    padding: 12,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: '#E8D5C0',
  },
  contactHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 10,
    gap: 6,
  },
  contactHeaderText: {
    fontSize: 15,
    fontWeight: '600',
    color: COLORS.primary,
  },
  contactRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 8,
    borderTopWidth: 1,
    borderTopColor: '#E8D5C0',
    gap: 8,
  },
  contactText: {
    flex: 1,
    fontSize: 14,
    color: '#333',
  },
  // Locked state
  lockedCard: {
    backgroundColor: '#f9f6f2',
    borderRadius: 10,
    padding: 12,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: '#E8D5C0',
  },
  lockedContent: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
    gap: 8,
  },
  lockedText: {
    flex: 1,
    fontSize: 14,
    color: '#777',
  },
  feeBadge: {
    backgroundColor: COLORS.primary,
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: 12,
  },
  feeBadgeText: {
    color: '#fff',
    fontSize: 12,
    fontWeight: '600',
  },
  revealButton: {
    backgroundColor: '#E8A14A',
    paddingVertical: 12,
    borderRadius: 10,
    alignItems: 'center',
  },
  revealButtonText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: 'bold',
  },
  payCatalogue: {
    flexDirection: 'row',
    justifyContent: 'center',
    alignItems: 'center',
  },
  catalogue: {
    fontSize: 14,
    color: '#007BFF',
    textAlign: 'center',
  },
});
