import React, { useContext, useState } from "react";
import {
  ScrollView,
  View,
  Text,
  Image,
  StyleSheet,
  Pressable,
} from "react-native";
import { COLORS, SIZES } from "@/constants";
import { router } from "expo-router";
import { AppContext } from "@/AuthContext/AuthContext";
import NotFound from "../others/NotFound";

interface Item {
  id: string;
  title: string;
  icon: any;
}

interface ListCardProps {
  itemList: Item[];
  itemValue: string;
}

const ListCard: React.FC<ListCardProps> = ({ itemList, itemValue }) => {
  const { setCategory } = useContext(AppContext);
  const [isDisabled, setIsDisabled] = useState(false);

  return (
    <ScrollView style={styles.container}>
      {itemList.length === 0 ? (
        <NotFound message="No items found." />
      ) : (
        itemList.map((item, itemIndex) => (
          <Pressable
            onPress={() => {
              if (itemValue === "digital") {
                return alert("coming soon");
              }
              if (!isDisabled) {
                setIsDisabled(true);
                setCategory(item.title);
                router.push({
                  //@ts-ignore
                  pathname: `/${itemValue}`,
                  params: { value: item.id },
                });
                setTimeout(() => {
                  setIsDisabled(false);
                }, 2000);
              }
            }}
            key={itemIndex}
            disabled={isDisabled}
          >
            <View style={styles.itemContainer}>
              <Image
                source={item.icon}
                style={styles.iconStyle}
                resizeMode="contain"
              />
              <Text style={styles.itemText}>{item.title}</Text>
            </View>
          </Pressable>
        ))
      )}
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
    backgroundColor: "#FFF4E8",
    padding: 10,
  },
  iconStyle: {
    width: 40,
    height: 40,
    marginRight: 10,
    borderRadius: 60,
  },
  itemText: {
    fontSize: 16,
  },
});

export default ListCard;
