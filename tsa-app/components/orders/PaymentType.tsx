import {
  Image,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from "react-native";
import React, { useState } from "react";
import { COLORS, SIZES } from "../../constants";
import { baseUrl } from "../../constants/api/apiClient";
import { useAuth } from "../../AuthContext/AuthContext";
import { router } from "expo-router";
import axios from "axios";
interface PaymentSelectionProps {
  address: string;
  state: string;
  city: string;
  country: string;
  phoneNumber: string;
  totalAmount: string;
  fullName: string;
}
const PaymentSelection: React.FC<PaymentSelectionProps> = ({
  address,
  state,
  city,
  country,
  phoneNumber,
  totalAmount,
  fullName,
}) => {
  const [selectedPaymentType, setSelectedPaymentType] = useState("Fiat");
  const [amount, setAmount] = useState(totalAmount);
  const { token, tTy } = useAuth();
  const [currency, setCurrency] = useState("");
  const [countUSDT, setCountUSDT] = useState(0);
  const [countMCGP, setCountMCGP] = useState(0);

  const placeOrder = async (payLoad: any) => {
    try {
      const response = await axios.post(`${baseUrl}/order`, payLoad, {
        headers: {
          Authorization: `${token}`,
          "Content-Type": "application/json",
        },
      });
      if (response.status === 201) {
        return { data: response.data, message: "success", success: true };
      }
    } catch (error: any) {
      return {
        data: "",
        message: error.response?.data?.message,
        success: false,
      };
    }
  };
  const paymentMethods = async () => {
    try {
      const response = await axios.get(`${baseUrl}/payment-methods`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      if (response.status === 200) {
        return { data: response.data, message: "success", success: true };
      }
    } catch (error: any) {
      return {
        data: "",
        message: error.response?.data?.message,
        success: false,
      };
    }
  };
  return (
    <View style={styles.cover}>
      <View style={{ marginTop: 40 }}>
        <View style={{ alignItems: "center" }}>
          <View
            style={{
              flexDirection: "row",
              width: SIZES.width * 0.60217,
              height: SIZES.height * 0.06686695,
              backgroundColor: "#DDA15E",
              alignItems: "center",
              justifyContent: "center",
              borderWidth: 1,
              borderColor: "#DDA15E",
              marginBottom: 50,
              borderRadius: 3,
            }}
          >
            <TouchableOpacity
              style={[
                styles.toggleButton,
                selectedPaymentType === "Fiat" && styles.selectedToggleButton,
              ]}
              onPress={() => setSelectedPaymentType("Fiat")}
            >
              <Text style={styles.toggleButtonText}>Fiat</Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[
                styles.toggleButton,
                selectedPaymentType === "Crypto" && styles.selectedToggleButton,
              ]}
              onPress={() => setSelectedPaymentType("Crypto")}
            >
              <Text style={styles.toggleButtonText}>Crypto</Text>
            </TouchableOpacity>
          </View>
        </View>
        {selectedPaymentType === "Fiat" && (
          <>
            <TextInput
              style={styles.input}
              placeholder="Enter Amount"
              value={amount}
              onChangeText={setAmount}
              keyboardType="numeric"
              editable={false}
            />
            <View style={styles.itemContainer}>
              <Image
                source={require("../../assets/icons/bank.png")}
                style={styles.iconStyle}
                resizeMode="contain"
              />
              <Text style={styles.itemText}>Bank Transfer</Text>
            </View>
            <TouchableOpacity
              style={[styles.button]}
              onPress={async () => {
                const result: any = await placeOrder({
                  deliveryAddress: {
                    address,
                    state,
                    city,
                    country,
                    phoneNumber,
                    fullName,
                  },
                  paymentMethod: "fiat",
                });
                //@ts-ignore
                if (result.success) {
                  router.push({
                    pathname: "/paymentdetail",
                    params: {
                      bankName: result.data.paymentDetails.bankName,
                      accountName: result.data.paymentDetails.accountName,
                      accountNumber: result.data.paymentDetails.accountNumber,
                      id: result.data.id,
                    },
                  });
                } else {
                  alert(result.message);
                }
              }}
              /*  disabled={loading} */
            >
              <Text style={styles.buttonText}>
                {tTy ? "Pay System Fee" : "Buy Product"}
              </Text>
            </TouchableOpacity>
          </>
        )}
        {selectedPaymentType === "Crypto" && (
          <>
            <TextInput
              style={styles.input}
              placeholder="Enter Amount"
              value={amount}
              onChangeText={setAmount}
              keyboardType="numeric"
              editable={false}
            />
            <Pressable
              onPress={() => {
                if (countUSDT > 0) {
                  setCurrency("");
                  setCountUSDT(0);
                  setCountMCGP(0);
                } else {
                  setCountUSDT(1);
                  setCurrency("USDT");
                }
              }}
            >
              <View style={styles.itemContainer}>
                <Image
                  source={require("../../assets/icons/usdt.png")}
                  style={styles.iconStyle}
                  resizeMode="contain"
                />
                <Text style={styles.itemText}>USDT</Text>
                {currency === "USDT" && (
                  <Text
                    style={{ position: "absolute", right: 10, color: "green" }}
                  >
                    ✔️
                  </Text>
                )}
              </View>
            </Pressable>
            <Pressable
              onPress={() => {
                if (countMCGP > 0) {
                  setCurrency("");
                  setCountMCGP(0);
                  setCountUSDT(0);
                } else {
                  setCountMCGP(1);
                  setCurrency("MCGP");
                }
              }}
            >
              <View style={styles.itemContainer}>
                <Image
                  source={require("../../assets/digital/mcgp.png")}
                  style={styles.iconStyle}
                  resizeMode="contain"
                />
                <Text style={styles.itemText}>MCGP</Text>
                {currency === "MCGP" && (
                  <Text
                    style={{ position: "absolute", right: 10, color: "green" }}
                  >
                    ✔️
                  </Text>
                )}
              </View>
            </Pressable>
            <TouchableOpacity
              style={[styles.button]}
              onPress={async () => {
                if (!currency) {
                  return alert("Please select currency");
                }
                const response: any = await paymentMethods();
                const result: any = await placeOrder({
                  deliveryAddress: {
                    address,
                    state,
                    city,
                    country,
                    phoneNumber,
                    fullName,
                  },
                  paymentMethod: "crypto",
                });
                if (result.success) {
                  if (response.success && response.data.length > 0) {
                    const paymentDetails = response.data.find(
                      (method: any) => method.symbol === currency
                    );

                    router.push({
                      pathname: "/cryptodetails",
                      params: {
                        walletAddress: paymentDetails.walletAddress,
                        network: paymentDetails.network,
                        symbol: paymentDetails.symbol,
                        id: result.data.id,
                      },
                    });
                  }
                } else {
                  alert(result.message);
                }
              }}
            >
              <Text style={styles.buttonText}>
                {tTy ? "Pay System Fee" : "Buy Product"}
              </Text>
            </TouchableOpacity>
          </>
        )}
      </View>
      <View style={{ alignItems: "center" }}></View>
    </View>
  );
};
export default PaymentSelection;

const styles = StyleSheet.create({
  container: {
    justifyContent: "center",
    padding: 20,
    backgroundColor: COLORS.white,
    alignItems: "center",
  },
  input: {
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
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: "center",
    justifyContent: "center",
    height: 0.0687 * SIZES.height,
    borderRadius: 20,
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginHorizontal: SIZES.width * 0.05,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  cover: {
    backgroundColor: COLORS.white,
    height: SIZES.height,
  },
  label: {
    marginHorizontal: SIZES.width * 0.05,
  },
  priceRangeContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 15,
  },

  toggleButton: {
    flex: 1,
    paddingVertical: 10,
    paddingHorizontal: 20,
    backgroundColor: "#DDA15E",
    alignItems: "center",
    borderRadius: 5,
    margin: 3,
  },
  selectedToggleButton: {
    backgroundColor: "#fff",
  },
  toggleButtonText: {
    color: "#000",
    fontSize: 14,
  },
  itemContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 10,
    borderColor: COLORS.gray,
    borderBottomWidth: 0.5,
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    backgroundColor: "#FFF4E8",
    marginHorizontal: SIZES.width * 0.05,
    borderRadius: 5,
  },

  iconStyle: {
    margin: 10,
    resizeMode: "contain",
  },
  itemText: {
    fontSize: 16,
  },
});
