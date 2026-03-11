import React from "react";
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Image,
} from "react-native";
import { COLORS, SIZES } from "../../constants";
import { router } from "expo-router";
import { LinearGradient } from "expo-linear-gradient";
import { useAuth } from "../../AuthContext/AuthContext";

interface Service {
  name: string;
  icon: any;
}

const services: Service[] = [
  {
    name: "Register your product",
    icon: require("../../assets/appservices/product.png"),
  },
  {
    name: "Register your service",
    icon: require("../../assets/appservices/service.png"),
  },
  {
    name: "Apply as merchant for payment service",
    icon: require("../../assets/appservices/apply.png"),
  },
  { name: "Buy product", icon: require("../../assets/appservices/buy.png") },
  {
    name: "Order service",
    icon: require("../../assets/appservices/order.png"),
  },
  { name: "Deposit", icon: require("../../assets/appservices/deposit.png") },
  { name: "Withdraw", icon: require("../../assets/appservices/withdraw.png") },
 /*  { name: "Stake USDT", icon: require("../../assets/appservices/stake.png") },
  { name: "Stake MCGP", icon: require("../../assets/appservices/stake.png") },
  {
    name: "Trade digital asset",
    icon: require("../../assets/appservices/trade.png"),
  }, */
  { name: "Wallet", icon: require("../../assets/appservices/wallet.png") },
];

const AppServiceList: React.FC = () => {
  const { setAppService } = useAuth();

  return (
    <ScrollView style={styles.container}>
      {services.map((service, index) => (
        <TouchableOpacity
          key={index}
          onPress={() => {
            if (index === 0 || index === 1) {
              setAppService(service.name);
              //@ts-ignore
              router.push({ pathname: "/serviceaction", params: { index } });
            } else {
              if (index === 3) {
                //@ts-ignore
                router.push("/products");
              } else if (index === 4) {
                //@ts-ignore
                router.push("/service");
              }else if (index === 5) {
                //@ts-ignore
                router.push("/sellp2p");
              }
              
              else {
                alert("Service coming soon");
              }
            }
          }}
        >
          <LinearGradient
            colors={["#FFF5EA", "#E8A14A"]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0 }}
            style={styles.button}
          >
            <Image source={service.icon} style={styles.icon} />
            <Text style={styles.text}>{service.name}</Text>
            <Image
              source={require("../../assets/appservices/nav.png")}
              style={[styles.navIcon, { tintColor: COLORS.black }]}
            />
          </LinearGradient>
        </TouchableOpacity>
      ))}
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f5f5f5",
  },
  button: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#FFF5EA",
    padding: 10,
    marginVertical: 3,
    marginHorizontal: 10,
    borderRadius: 5,
    height: SIZES.height * 0.06437768,
  },
  icon: {
    marginRight: 10,
    color: COLORS.black,
    height: 27,
    width: 27,
    resizeMode: "contain",
  },
  text: {
    color: COLORS.black,
    fontSize: 14,
    fontWeight: "400",
  },
  navIcon: {
    position: "absolute",
    right: 10,
  },
});

export default AppServiceList;
