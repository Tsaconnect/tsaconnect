// PickerWithSearch.tsx (Updated to support searchable prop)
import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  Modal,
  FlatList,
  TextInput,
  StyleSheet,
} from 'react-native';
import { COLORS, SIZES, FONTS } from '../constants/theme';

type PickerItem = {
  label: string;
  value: string;
};

type PickerWithSearchProps = {
  data: PickerItem[];
  selectedValue: string;
  onSelect: (item: PickerItem) => void;
  placeholder?: string;
  style?: object;
  searchable?: boolean;
};

const PickerWithSearch = ({
  data,
  selectedValue,
  onSelect,
  placeholder = 'Select an option',
  style,
  searchable = true,
}: PickerWithSearchProps) => {
  const [modalVisible, setModalVisible] = useState(false);
  const [searchText, setSearchText] = useState('');

  useEffect(() => {
    if (data.length === 1 && selectedValue !== data[0].value) {
      onSelect(data[0]);
    }
  }, [data]);

  const selectedItem = data.find(item => item.value === selectedValue);
  
  const filteredData = searchable
    ? data.filter(item =>
        item.label.toLowerCase().includes(searchText.toLowerCase()) ||
        item.value.toLowerCase().includes(searchText.toLowerCase())
      )
    : data;
  
  const handleSelect = (item: PickerItem) => {
    onSelect(item);
    setModalVisible(false);
    setSearchText('');
  };
  
  return (
    <View>
      <TouchableOpacity
        style={[styles.pickerButton, style]}
        onPress={() => data.length > 1 && setModalVisible(true)}
      >
        <Text style={styles.pickerText}>
          {selectedItem ? selectedItem.label : placeholder}
        </Text>
        <Text style={styles.dropdownIcon}>▼</Text>
      </TouchableOpacity>
      
      <Modal
        visible={modalVisible}
        transparent
        animationType="slide"
        onRequestClose={() => {
          setModalVisible(false);
          setSearchText('');
        }}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            {/* Header */}
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Select Asset</Text>
              <TouchableOpacity onPress={() => setModalVisible(false)}>
                <Text style={styles.closeButton}>✕</Text>
              </TouchableOpacity>
            </View>
            
            {/* Search Input (conditional) */}
            {searchable && (
              <TextInput
                style={styles.searchInput}
                placeholder="Search assets..."
                value={searchText}
                onChangeText={setSearchText}
                autoCapitalize="none"
              />
            )}
            
            {/* Options List */}
            <FlatList
              data={filteredData}
              nestedScrollEnabled
              keyExtractor={(item) => item.value}
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={[
                    styles.optionItem,
                    selectedValue === item.value && styles.selectedOption,
                  ]}
                  onPress={() => handleSelect(item)}
                >
                  <Text
                    style={[
                      styles.optionText,
                      selectedValue === item.value && styles.selectedOptionText,
                    ]}
                  >
                    {item.label}
                  </Text>
                </TouchableOpacity>
              )}
              ItemSeparatorComponent={() => <View style={styles.separator} />}
            />
          </View>
        </View>
      </Modal>
    </View>
  );
};

const styles = StyleSheet.create({
  pickerButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderWidth: 1,
    borderColor: COLORS.gray,
    borderRadius: SIZES.radius,
    paddingHorizontal: SIZES.base,
    paddingVertical: SIZES.base * 0.8,
    backgroundColor: COLORS.white,
  },
  pickerText: {
    ...FONTS.body4,
    color: COLORS.black,
  },
  dropdownIcon: {
    ...FONTS.body4,
    color: COLORS.gray,
    marginLeft: SIZES.base,
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'center',
    alignItems: 'center',
  },
  modalContent: {
    backgroundColor: COLORS.white,
    borderRadius: SIZES.radius * 2,
    width: '80%',
    maxHeight: '60%',
    overflow: 'hidden',
  },
  modalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: SIZES.padding,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.grey,
  },
  modalTitle: {
    ...FONTS.h4,
    color: COLORS.black,
    fontWeight: 'bold',
  },
  closeButton: {
    ...FONTS.h4,
    color: COLORS.gray,
  },
  searchInput: {
    ...FONTS.body4,
    borderWidth: 1,
    borderColor: COLORS.grey,
    borderRadius: SIZES.radius,
    paddingHorizontal: SIZES.base,
    paddingVertical: SIZES.base * 0.8,
    margin: SIZES.padding,
  },
  optionItem: {
    paddingHorizontal: SIZES.padding,
    paddingVertical: SIZES.base * 1.5,
  },
  selectedOption: {
    backgroundColor: COLORS.primary + '10',
  },
  optionText: {
    ...FONTS.body4,
    color: COLORS.black,
  },
  selectedOptionText: {
    color: COLORS.primary,
    fontWeight: 'bold',
  },
  separator: {
    height: 1,
    backgroundColor: COLORS.grey,
  },
});

export { PickerWithSearch };