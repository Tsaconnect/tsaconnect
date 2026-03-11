// components/country/dropdown.tsx
import React, { useState } from 'react';
import {
  View,
  TextInput,
  TouchableOpacity,
  FlatList,
  Text,
  StyleSheet,
  Modal,
} from 'react-native';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';

interface CustomPickerWithSearchProps {
  data: any[];
  selectedItem: string;
  setSelectedItem: (item: string) => void;
  backgroundColor?: string;
  borderColor?: string;
  borderWidth?: number;
  placeholder?: string;
}

const CustomPickerWithSearch: React.FC<CustomPickerWithSearchProps> = ({
  data,
  selectedItem,
  setSelectedItem,
  backgroundColor = '#FFF',
  borderColor = COLORS.lightGray,
  borderWidth = 1,
  placeholder = 'Select an option',
}) => {
  const [modalVisible, setModalVisible] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');

  const filteredData = data.filter(item =>
    item.toLowerCase().includes(searchQuery.toLowerCase())
  );

  return (
    <>
      <TouchableOpacity
        style={[
          styles.container,
          {
            backgroundColor,
            borderColor,
            borderWidth,
          },
        ]}
        onPress={() => setModalVisible(true)}
      >
        <Text style={[styles.selectedText, !selectedItem && styles.placeholder]}>
          {selectedItem || placeholder}
        </Text>
        <MaterialIcons name="arrow-drop-down" size={24} color={COLORS.gray} />
      </TouchableOpacity>

      <Modal
        visible={modalVisible}
        animationType="slide"
        transparent
        onRequestClose={() => setModalVisible(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>{placeholder}</Text>
              <TouchableOpacity onPress={() => setModalVisible(false)}>
                <MaterialIcons name="close" size={24} color={COLORS.dark} />
              </TouchableOpacity>
            </View>

            <View style={styles.searchContainer}>
              <MaterialIcons name="search" size={20} color={COLORS.gray} />
              <TextInput
                style={styles.searchInput}
                placeholder="Search..."
                value={searchQuery}
                onChangeText={setSearchQuery}
              />
            </View>

            <FlatList
              data={filteredData}
              keyExtractor={(item, index) => index.toString()}
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={[
                    styles.listItem,
                    selectedItem === item && styles.selectedListItem,
                  ]}
                  onPress={() => {
                    setSelectedItem(item);
                    setModalVisible(false);
                    setSearchQuery('');
                  }}
                >
                  <Text
                    style={[
                      styles.listItemText,
                      selectedItem === item && styles.selectedListItemText,
                    ]}
                  >
                    {item}
                  </Text>
                  {selectedItem === item && (
                    <MaterialIcons name="check" size={20} color={COLORS.primary} />
                  )}
                </TouchableOpacity>
              )}
              ListEmptyComponent={
                <View style={styles.emptyContainer}>
                  <MaterialIcons name="search-off" size={40} color={COLORS.gray} />
                  <Text style={styles.emptyText}>No results found</Text>
                </View>
              }
            />
          </View>
        </View>
      </Modal>
    </>
  );
};

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderRadius: 12,
    paddingHorizontal: 15,
    height: 56,
  },
  selectedText: {
    fontSize: 16,
    color: COLORS.dark,
  },
  placeholder: {
    color: COLORS.gray,
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'flex-end',
  },
  modalContent: {
    backgroundColor: '#fff',
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: '80%',
  },
  modalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: 20,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  modalTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.dark,
  },
  searchContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: COLORS.background,
    margin: 20,
    paddingHorizontal: 15,
    borderRadius: 12,
    height: 50,
  },
  searchInput: {
    flex: 1,
    marginLeft: 10,
    fontSize: 16,
  },
  listItem: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingVertical: 15,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  selectedListItem: {
    backgroundColor: 'rgba(99, 102, 241, 0.1)',
  },
  listItemText: {
    fontSize: 16,
    color: COLORS.dark,
  },
  selectedListItemText: {
    color: COLORS.primary,
    fontWeight: '600',
  },
  emptyContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    padding: 40,
  },
  emptyText: {
    marginTop: 10,
    color: COLORS.gray,
    fontSize: 16,
  },
});

export default CustomPickerWithSearch;