import React from "react";
import { ScrollView, View, Text, Image, StyleSheet } from "react-native";
import { COLORS, SIZES } from "../../constants";
import { Pressable } from "react-native";

// Example Data
const categories = [
  { title: "MCGP", icon: require("../../assets/digital/mcgp.png") },
  { title: "Deficonnect", icon: require("../../assets/digital/dfc.png") },
  { title: "Binance", icon: require("../../assets/digital/binance.png") },
  { title: "Solana", icon: require("../../assets/digital/solana.png") },
  { title: "Dogecoin", icon: require("../../assets/digital/dogecoin.png") },
];

const DigitalList = () => {
  return (
    <ScrollView style={styles.container}>
      {categories.map((item, itemIndex) => (
        <Pressable key={itemIndex} onPress={() => alert("Service coming soon")}>
          <View style={styles.itemContainer}>
            <Image
              source={item.icon}
              style={styles.iconStyle}
              resizeMode="contain"
            />
            <Text style={styles.itemText}>{item.title}</Text>
          </View>
        </Pressable>
      ))}
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 10,
    backgroundColor: "#fff",
  },
  itemContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 5,
    borderColor: COLORS.gray,
    borderBottomWidth: 0.5,
    height: SIZES.height * 0.07081545,
  },
  iconStyle: {
    width: 50,
    height: "90%",
    marginRight: 10,
  },
  itemText: {
    fontSize: 16,
  },
});

export default DigitalList;
