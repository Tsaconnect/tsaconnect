// components/country/dropdowns.tsx
import React, { useState, useEffect } from 'react';
import {
  View,
  TextInput,
  TouchableOpacity,
  FlatList,
  Text,
  StyleSheet,
  Modal,
  ActivityIndicator,
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
  disabled?: boolean;
  loading?: boolean;
}

const CustomPickerWithSearch: React.FC<CustomPickerWithSearchProps> = ({
  data,
  selectedItem,
  setSelectedItem,
  backgroundColor = '#FFF',
  borderColor = COLORS.lightGray,
  borderWidth = 1,
  placeholder = 'Select an option',
  disabled = false,
  loading = false,
}) => {
  const [modalVisible, setModalVisible] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');

  useEffect(() => {
    if (data.length === 1 && selectedItem !== data[0]) {
      setSelectedItem(data[0]);
    }
  }, [data]);

  const filteredData = data.filter(item =>
    item.toLowerCase().includes(searchQuery.toLowerCase())
  );

  return (
    <>
      <TouchableOpacity
        style={[
          styles.container,
          {
            backgroundColor: disabled ? COLORS.background : backgroundColor,
            borderColor: disabled ? COLORS.lightGray : borderColor,
            borderWidth,
            opacity: disabled || loading ? 0.6 : 1,
          },
        ]}
        onPress={() => !disabled && !loading && setModalVisible(true)}
        disabled={disabled || loading}
      >
        <Text style={[
          styles.selectedText, 
          !selectedItem && styles.placeholder,
          (disabled || loading) && { color: COLORS.gray }
        ]}>
          {selectedItem || placeholder}
        </Text>
        {loading ? (
          <ActivityIndicator size="small" color={COLORS.primary} />
        ) : (
          <MaterialIcons 
            name="arrow-drop-down" 
            size={24} 
            color={disabled ? COLORS.gray : COLORS.primary} 
          />
        )}
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

            {/* Search Input */}
            <View style={styles.searchContainer}>
              <MaterialIcons name="search" size={20} color={COLORS.gray} />
              <TextInput
                style={styles.searchInput}
                placeholder="Search..."
                value={searchQuery}
                onChangeText={setSearchQuery}
                autoFocus
              />
            </View>

            {/* Empty State */}
            {filteredData.length === 0 ? (
              <View style={styles.emptyContainer}>
                <MaterialIcons name="search-off" size={40} color={COLORS.gray} />
                <Text style={styles.emptyText}>
                  {searchQuery ? 'No results found' : 'No options available'}
                </Text>
              </View>
            ) : (
              <FlatList
                data={filteredData}
                nestedScrollEnabled
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
              />
            )}
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
    marginTop: 0,
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