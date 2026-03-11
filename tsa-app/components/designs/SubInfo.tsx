import { View, Text, Image, ImageSourcePropType } from "react-native";
import React from "react";
import { COLORS, SIZES, SHADOWS, assets } from "../../constants";

// Define the prop types for each component
type ProductTitleProps = {
  title: string;
  titleSize: number;
  companyName: string;
};

type CatalogueProps = {
  handlePress: () => void;
};

type ImageCmpProps = {
  imgUrl: ImageSourcePropType;
  index: number;
};

type PriceProps = {
  price: number;
};

type SubInfoProps = {
  price: number;
};

// Component for Product Title
export const ProductTitle: React.FC<ProductTitleProps> = ({
  title,
  titleSize,
  companyName,
}) => {
  return (
    <View>
      <Text
        style={{
          fontSize: titleSize,
          color: COLORS.primary,
          fontWeight: "500",
        }}
      >
        {companyName}'s {title}
      </Text>
    </View>
  );
};

// Component for Catalogue
export const Catalogue: React.FC<CatalogueProps> = ({ handlePress }) => {
  return (
    <View style={{ flexDirection: "row", alignItems: "center" }}>
      <Text
        onPress={handlePress}
        style={{
          fontSize: SIZES.small,
          color: COLORS.black,
          fontWeight: "500",
        }}
      >
        see catalogue
      </Text>
    </View>
  );
};

// Component for displaying an image
export const ImageCmp: React.FC<ImageCmpProps> = ({ imgUrl, index }) => {
  return (
    <View>
      <Image
        source={imgUrl}
        resizeMode="contain"
        style={{
          width: 48,
          height: 48,
          marginLeft: index === 0 ? 0 : -SIZES.font,
        }}
      />
    </View>
  );
};

// Component for displaying people avatars
export const People: React.FC = () => {
  const personsArray: ImageSourcePropType[] = [
    assets.person01,
    assets.person02,
    assets.person03,
    assets.person04,
  ];

  return (
    <View style={{ flexDirection: "row" }}>
      {personsArray.map((imgUrl, index) => (
        <ImageCmp imgUrl={imgUrl} index={index} key={`People-${index}`} />
      ))}
    </View>
  );
};

// Component for displaying price
export const Price: React.FC<PriceProps> = ({ price }) => {
  return (
    <View
      style={{
        paddingHorizontal: SIZES.font,
        paddingVertical: SIZES.base,
        backgroundColor: COLORS.white,
        justifyContent: "center",
        alignItems: "center",
        ...SHADOWS.light,
        elevation: 1,
        maxWidth: "50%",
      }}
    >
      <Text
        style={{
          fontSize: SIZES.small,
          color: COLORS.black,
        }}
      >
        Amount in NGN
      </Text>
      <Text
        style={{
          fontSize: SIZES.large,
          color: COLORS.black,
          fontWeight: "600",
        }}
      >
        ₦{price}
      </Text>
    </View>
  );
};

// Component for sub-information display
export const SubInfo: React.FC<SubInfoProps> = ({ price }) => {
  return (
    <View
      style={{
        width: "100%",
        paddingHorizontal: SIZES.font,
        marginTop: -SIZES.extraLarge,
        flexDirection: "row",
        justifyContent: "space-between",
      }}
    >
      <People />
      <Price price={price} />
    </View>
  );
};
