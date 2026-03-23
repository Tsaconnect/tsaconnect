import React, { useState, useEffect } from "react";
import {
  View,
  TextInput,
  TouchableOpacity,
  Text,
  FlatList,
  StyleSheet,
  Modal,
} from "react-native";
import { COLORS, SIZES } from "../../constants";

interface DropDownStateProps {
  data: { name: string }[];
  selectedItem: string | null;
  setSelectedItem: (item: string) => void;
  getCity?: (location: { country: string; state: string }) => void;
  country: string;
}

const DropDownState: React.FC<DropDownStateProps> = ({
  data,
  selectedItem,
  setSelectedItem,
  getCity,
  country,
}) => {
  const [searchQuery, setSearchQuery] = useState("");
  const [filteredData, setFilteredData] = useState(data);
  const [isDropdownOpen, setIsDropdownOpen] = useState(false);

  useEffect(() => {
    setFilteredData(data);
    if (data.length === 1 && selectedItem !== data[0].name) {
      handleSelectItem(data[0].name);
    }
  }, [data]);

  const handleSearch = (text: string) => {
    setSearchQuery(text);
    const filtered = data.filter((item) =>
      item.name.toLowerCase().includes(text.toLowerCase())
    );
    setFilteredData(filtered);
  };

  const handleSelectItem = (item: string) => {
    setSelectedItem(item);
    setIsDropdownOpen(false);
    setSearchQuery("");
    setFilteredData(data);
    if (getCity) {
      getCity({ country: country, state: item });
    }
  };

  return (
    <View style={styles.container}>
      <TouchableOpacity
        style={styles.pickerContainer}
        onPress={() => setIsDropdownOpen(true)}
      >
        <Text style={styles.selectedItemText}>
          {selectedItem || "Select a State"}
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
              nestedScrollEnabled
              keyExtractor={(item, index) => index.toString()}
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={styles.itemContainer}
                  onPress={() => handleSelectItem(item.name)}
                >
                  <Text style={styles.itemText}>{item.name}</Text>
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
    backgroundColor: COLORS.gray,
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

export default DropDownState;
