// components/designs/SubInfo.tsx
//
// Sub-info row primitives (ProductTitle, Catalogue, ImageCmp, People, Price,
// SubInfo) used on product detail / card layouts. Migrated onto the TSA
// Signature design system (theme tokens + @/components/ui primitives). Every
// prop type, export, the `onPress`/`handlePress` wiring, the avatar image set,
// the negative-margin overlap layout, and the literal price text are preserved
// verbatim — this is a visual / structural restyle only.
import React from "react";
import { View, Image, ImageSourcePropType } from "react-native";
import { useTheme } from "@/theme";
import { useCurrency } from "@/contexts/CurrencyContext";
import { Surface, Text } from "@/components/ui";
import { assets } from "../../constants";

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
      <Text color="accent" weight="500" style={{ fontSize: titleSize }}>
        {companyName}&apos;s {title}
      </Text>
    </View>
  );
};

// Component for Catalogue
export const Catalogue: React.FC<CatalogueProps> = ({ handlePress }) => {
  return (
    <View style={{ flexDirection: "row", alignItems: "center" }}>
      <Text onPress={handlePress} variant="caption" weight="500">
        see catalogue
      </Text>
    </View>
  );
};

// Component for displaying an image
export const ImageCmp: React.FC<ImageCmpProps> = ({ imgUrl, index }) => {
  const { spacing } = useTheme();
  return (
    <View>
      <Image
        source={imgUrl}
        resizeMode="contain"
        style={{
          width: 48,
          height: 48,
          marginLeft: index === 0 ? 0 : -spacing.md,
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

// Component for displaying price. `price` is a USD-anchored figure (product
// prices are stored/returned in USD — see tsa-app/CLAUDE.md); render it in the
// user's selected display currency via CurrencyContext instead of the
// previously hardcoded "Amount in NGN" label paired with a "$" value (a
// stale/mismatched leftover — the label and the symbol never agreed).
export const Price: React.FC<PriceProps> = ({ price }) => {
  const { spacing } = useTheme();
  const { formatPrice, currency } = useCurrency();
  return (
    <Surface
      elevation="sm"
      radius="md"
      style={{
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.sm,
        justifyContent: "center",
        alignItems: "center",
        maxWidth: "50%",
      }}
    >
      <Text variant="caption">Amount in {currency.code}</Text>
      <Text variant="title3" weight="600">
        {formatPrice(price)}
      </Text>
    </Surface>
  );
};

// Component for sub-information display
export const SubInfo: React.FC<SubInfoProps> = ({ price }) => {
  const { spacing } = useTheme();
  return (
    <View
      style={{
        width: "100%",
        paddingHorizontal: spacing.md,
        marginTop: -spacing["2xl"],
        flexDirection: "row",
        justifyContent: "space-between",
      }}
    >
      <People />
      <Price price={price} />
    </View>
  );
};
