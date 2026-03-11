import {
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
  ToastAndroid
} from "react-native";
import React from "react";
import { COLORS, SIZES } from "../../constants";
import { Image } from "react-native";
import { router } from "expo-router";
import * as Clipboard from "expo-clipboard";
import { SafeAreaView } from "react-native-safe-area-context";
const CryptoDetails = ({ walletAddress, symbol, network, id }) => {
  const copyToClipboard = async () => {
    await Clipboard.setStringAsync(walletAddress);
    ToastAndroid.show('Copied!', ToastAndroid.LONG);
  };
  return (
    <SafeAreaView
      style={{
        flex: 1,
        justifyContent: "center",
        alignItems: "center",
        backgroundColor: COLORS.white,
      }}
    >
      <View style={{ marginTop: 200 }}>
        <View style={styles.bankDetails}>
          <Text style={styles.input}> {walletAddress}</Text>
          <TouchableOpacity
            onPress={copyToClipboard}
            style={{ position: "absolute", right: 10 }}
          >
            <Image
              source={require("../../assets/icons/copy.png")}
              resizeMode="contain"
              style={{ marginRight: 20 }}
            />
          </TouchableOpacity>
        </View>

        <Text style={{ position: "absolute", padding: 50 }}>
          Send only {symbol}-{network} network to the wallet address above
        </Text>
        <TouchableOpacity
          style={[styles.button]}
          onPress={() => {
            router.push({ pathname: "/paymentproof", params: { id } });
          }}
        >
          <Text style={styles.buttonText}>Proceed</Text>
        </TouchableOpacity>
      </View>
    </SafeAreaView>
  );
};

export default CryptoDetails;

const styles = StyleSheet.create({
  bankDetails: {
    justifyContent: "space-between",
    flexDirection: "row",
    alignItems: "center",
  },
  input: {
    width: SIZES.width * 0.8,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
    marginLeft: SIZES.width * 0.05,
    backgroundColor: COLORS.gray,
    justifyContent: "space-between",
    flexDirection: "row",
    alignItems: "center",
    marginVertical: 1,
    paddingVertical: 10,
    fontSize: 13
  },
  labelText: {
    fontSize: 14,
    fontWeight: "400",
    color: "#9C9C9C",
    paddingLeft: 10,
  },
  valueText: {
    paddingRight: 10,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: "center",
    justifyContent: "center",
    height: 0.05042918 * SIZES.height,
    borderRadius: 20,
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginHorizontal: SIZES.width * 0.05,
    marginTop: 100,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
});
