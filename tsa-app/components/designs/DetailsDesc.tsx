import React, { useState } from "react";
import { View, Text } from "react-native";
import { Price, ProductTitle } from "./SubInfo";
import { COLORS, SIZES } from "../../constants";

// Define the type for the data prop
type DetailsDescProps = {
  data: {
    name: string;
    creator: string;
    description: string;
    price: number;
  };
};

const DetailsDesc: React.FC<DetailsDescProps> = ({ data }) => {
  const [text, setText] = useState(data.description.slice(0, 100));
  const [readMore, setReadMore] = useState(false);

  const readMoreHandler = () => {
    if (!readMore) {
      setText(data.description);
      setReadMore(true);
    } else {
      setText(data.description.slice(0, 100));
      setReadMore(false);
    }
  };

  return (
    <>
      {/* Title and Price Section */}
      <View
        style={{
          width: "100%",
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
        }}
      >
        <ProductTitle
          title={data.name}
          //@ts-ignore
          subTitle={data.creator}
          titleSize={SIZES.extraLarge}
          subTitleSize={SIZES.font}
        />
        <Price price={data.price} />
      </View>

      {/* Description Section */}
      <View style={{ marginVertical: SIZES.extraLarge * 1.5 }}>
        <Text style={{ fontSize: SIZES.font, color: COLORS.primary }}>
          Description
        </Text>
        <View style={{ marginTop: SIZES.base }}>
          <Text
            style={{
              fontSize: SIZES.small,
              color: COLORS.secondary,
              lineHeight: SIZES.large,
            }}
          >
            {text}
            {!readMore && "..."}
            <Text
              style={{ fontSize: SIZES.small, color: COLORS.primary }}
              onPress={readMoreHandler}
            >
              {readMore ? " Show Less" : " Read More"}
            </Text>
          </Text>
        </View>
      </View>
    </>
  );
};

export default DetailsDesc;
