// Star rating widget. Migrated to the TSA Signature design system:
// legacy FontAwesome glyphs + hardcoded #FFD700 are gone; stars now render via the
// semantic Icon primitive ("star"/"starOutline") tinted with colors.accent, and the
// row gap uses spacing tokens. The half-star is layered (outline + width-clipped fill)
// since the Icon registry has no half-star glyph. Props/exports and the
// filled/half/empty rating math are preserved verbatim.
import React from "react";
import { View } from "react-native";
import { Icon } from "@/components/ui";
import { useTheme } from "@/theme";

type StarRatingProps = {
  rating: number;
  maxStars?: number;
};

const STAR_SIZE = 20;

const StarRating: React.FC<StarRatingProps> = ({ rating, maxStars = 5 }) => {
  const { colors, spacing } = useTheme();

  const filledStars = Math.floor(rating);
  const halfStar = rating - filledStars >= 0.5;
  const emptyStars = maxStars - filledStars - (halfStar ? 1 : 0);

  return (
    <View style={{ flexDirection: "row", gap: spacing.xs / 2 }}>
      {Array.from({ length: filledStars }).map((_, index) => (
        <Icon
          key={`star-filled-${index}`}
          name="star"
          size={STAR_SIZE}
          color={colors.accent}
        />
      ))}
      {halfStar && (
        <View style={{ width: STAR_SIZE, height: STAR_SIZE }}>
          <Icon name="starOutline" size={STAR_SIZE} color={colors.accent} />
          <View
            style={{
              position: "absolute",
              left: 0,
              top: 0,
              bottom: 0,
              width: STAR_SIZE / 2,
              overflow: "hidden",
            }}
          >
            <Icon name="star" size={STAR_SIZE} color={colors.accent} />
          </View>
        </View>
      )}
      {Array.from({ length: emptyStars }).map((_, index) => (
        <Icon
          key={`star-empty-${index}`}
          name="starOutline"
          size={STAR_SIZE}
          color={colors.accent}
        />
      ))}
    </View>
  );
};

export default StarRating;
