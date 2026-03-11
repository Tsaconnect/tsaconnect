import React, { useState } from "react";
import {
  View,
  TextInput,
  TouchableOpacity,
  Text,
  FlatList,
  StyleSheet,
  Modal,
  ViewStyle,
} from "react-native";
import { COLORS, SIZES } from "../../constants";

interface CustomPickerWithSearchProps {
  data: string[];
  selectedItem: string | null;
  setSelectedItem: (item: string) => void;
  postData?: (data: { country: string }) => void;
  backgroundColor?: string;
  style?: ViewStyle;
}

const CustomPickerWithSearch: React.FC<CustomPickerWithSearchProps> = ({
  data,
  selectedItem,
  setSelectedItem,
  postData,
  backgroundColor,
  style,
  ...props
}) => {
  const [searchQuery, setSearchQuery] = useState("");
  const [filteredData, setFilteredData] = useState(data);
  const [isDropdownOpen, setIsDropdownOpen] = useState(false);

  const handleSearch = (text: string) => {
    setSearchQuery(text);
    const filtered = data.filter((item) =>
      item.toLowerCase().includes(text.toLowerCase())
    );
    setFilteredData(filtered);
  };

  const handleSelectItem = (item: string) => {
    setSelectedItem(item);
    setIsDropdownOpen(false);
    setSearchQuery("");
    setFilteredData(data);
    if (postData) {
      postData({ country: item });
    }
  };

  return (
    <View style={styles.container}>
      <TouchableOpacity
        style={[styles.pickerContainer, style, { backgroundColor }]}
        onPress={() => setIsDropdownOpen(true)}
        {...props}
      >
        <Text style={styles.selectedItemText}>
          {selectedItem || "Select a Country"}
        </Text>
      </TouchableOpacity>

      <Modal
        visible={isDropdownOpen}
        transparent={true}
        animationType="slide"
        onRequestClose={() => setIsDropdownOpen(false)}
      >
        <View style={styles.modalContainer}>
          <View style={styles.dropdownContainer}>
            <TextInput
              style={styles.input}
              placeholder="Search..."
              onChangeText={handleSearch}
              value={searchQuery}
            />
            <FlatList
              data={filteredData}
              keyExtractor={(item, index) => index.toString()}
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={styles.itemContainer}
                  onPress={() => handleSelectItem(item)}
                >
                  <Text style={styles.itemText}>{item}</Text>
                </TouchableOpacity>
              )}
            />
          </View>
        </View>
      </Modal>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {},
  pickerContainer: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
    marginTop: 2,
    marginBottom: 10,
    padding: 10,
    borderRadius: 5,
    marginHorizontal: SIZES.width * 0.05,
  },
  selectedItemText: {
    fontSize: 16,
  },
  modalContainer: {
    flex: 1,
    justifyContent: "flex-start",
    alignItems: "center",
    backgroundColor: "rgba(0, 0, 0, 0.5)",
    marginTop: 100,
  },
  dropdownContainer: {
    width: "80%",
    backgroundColor: "white",
    borderRadius: 5,
    padding: 10,
    shadowColor: "#000",
    shadowOffset: {
      width: 0,
      height: 2,
    },
    shadowOpacity: 0.25,
    shadowRadius: 3.84,
    elevation: 5,
  },
  input: {
    height: 40,
    borderColor: "gray",
    borderWidth: 1,
    marginBottom: 10,
    paddingHorizontal: 10,
  },
  itemContainer: {
    padding: 10,
  },
  itemText: {
    fontSize: 16,
  },
});

export default CustomPickerWithSearch;
