// components/p2p/PaymentMethodFilter.tsx
//
// Controlled "All Payment Methods" filter for the P2P browse screen. The
// parent supplies the provider options (derived from the loaded ads) and the
// current selection; this component only renders the trigger + picker sheet
// and reports the chosen provider slug (or null for "All").
import React, { useState } from 'react';
import {
  Modal,
  Pressable,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { P2P_GOLD } from '../../constants/p2p';

const GOLD = P2P_GOLD;

export interface PaymentProviderOption {
  provider: string;
  label: string;
}

interface PaymentMethodFilterProps {
  /** Provider options to choose from (e.g. from distinctAdProviders(ads)). */
  options: PaymentProviderOption[];
  /** Currently-selected provider slug, or null for "All Payment Methods". */
  selected: string | null;
  /** Called with the chosen provider slug, or null when "All" is picked. */
  onChange: (provider: string | null) => void;
}

const ALL_LABEL = 'All Payment Methods';

const PaymentMethodFilter: React.FC<PaymentMethodFilterProps> = ({
  options,
  selected,
  onChange,
}) => {
  const [open, setOpen] = useState(false);

  const selectedLabel =
    selected == null
      ? ALL_LABEL
      : options.find((o) => o.provider === selected)?.label ?? ALL_LABEL;

  const choose = (provider: string | null) => {
    onChange(provider);
    setOpen(false);
  };

  return (
    <>
      <TouchableOpacity
        style={styles.trigger}
        onPress={() => setOpen(true)}
        activeOpacity={0.7}
      >
        <Text style={styles.triggerText} numberOfLines={1}>
          {selectedLabel}
        </Text>
        <Icon name="arrow-drop-down" size={20} color={GOLD.dark} />
      </TouchableOpacity>

      <Modal visible={open} transparent animationType="slide" onRequestClose={() => setOpen(false)}>
        <Pressable style={styles.backdrop} onPress={() => setOpen(false)}>
          <Pressable style={styles.sheet} onPress={() => {}}>
            <View style={styles.header}>
              <Text style={styles.title}>Payment method</Text>
              <TouchableOpacity onPress={() => setOpen(false)} hitSlop={10}>
                <Icon name="close" size={24} color="#333" />
              </TouchableOpacity>
            </View>

            <Row
              label={ALL_LABEL}
              selected={selected == null}
              onPress={() => choose(null)}
            />
            {options.map((o) => (
              <Row
                key={o.provider}
                label={o.label}
                selected={selected === o.provider}
                onPress={() => choose(o.provider)}
              />
            ))}
          </Pressable>
        </Pressable>
      </Modal>
    </>
  );
};

const Row: React.FC<{ label: string; selected: boolean; onPress: () => void }> = ({
  label,
  selected,
  onPress,
}) => (
  <TouchableOpacity
    style={[styles.row, selected && styles.rowSelected]}
    onPress={onPress}
    activeOpacity={0.7}
  >
    <Text style={styles.rowText}>{label}</Text>
    {selected && <Icon name="check" size={20} color={GOLD.dark} />}
  </TouchableOpacity>
);

const styles = StyleSheet.create({
  trigger: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD.muted,
    backgroundColor: '#FFF',
    maxWidth: 200,
  },
  triggerText: { flex: 1, fontSize: 13, fontWeight: '600', color: GOLD.dark },
  backdrop: { flex: 1, backgroundColor: 'rgba(0,0,0,0.45)', justifyContent: 'flex-end' },
  sheet: {
    backgroundColor: '#FFF',
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: '70%',
    paddingBottom: 24,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingTop: 18,
    paddingBottom: 10,
  },
  title: { fontSize: 17, fontWeight: '700', color: '#000' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingVertical: 14,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  rowSelected: { backgroundColor: GOLD.light },
  rowText: { fontSize: 15, color: '#000' },
});

export default PaymentMethodFilter;
