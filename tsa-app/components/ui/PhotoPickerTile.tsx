// PhotoPickerTile — a row/grid of evidence/proof image tiles. Each uploaded
// image shows as a preview with a remove (×) button; a trailing "add" tile (+)
// lets the user pick a new image (expo-image-picker), uploads it to Cloudinary,
// and appends the returned URL to `value` via onChange.
//
// Uploads through the shared uploadImageFile (services/imageUpload).
import * as ImagePicker from 'expo-image-picker';
import React, { useState } from 'react';
import { Image, View } from 'react-native';
import { useTheme } from '@/theme';
import { uploadImageFile } from '@/services/imageUpload';
import { Icon } from './Icon';
import { PressableScale } from './PressableScale';
import { Spinner } from './Spinner';
import { Text } from './Text';
import { useToast } from './Toast';

export interface PhotoPickerTileProps {
  /** Uploaded image URLs. */
  value: string[];
  /** Called with the next list of URLs whenever an image is added or removed. */
  onChange: (urls: string[]) => void;
  /** Maximum number of images. Defaults to 6. */
  max?: number;
  /** Optional field label rendered above the tiles. */
  label?: string;
}

const TILE = 80;

export const PhotoPickerTile: React.FC<PhotoPickerTileProps> = ({
  value,
  onChange,
  max = 6,
  label,
}) => {
  const { colors, radius, spacing } = useTheme();
  const toast = useToast();
  const [uploading, setUploading] = useState(false);

  const canAdd = value.length < max;

  const remove = (idx: number) => {
    onChange(value.filter((_, i) => i !== idx));
  };

  const add = async () => {
    if (uploading) return;
    try {
      const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== 'granted') {
        toast.error('Allow photo library access to attach images.');
        return;
      }
      const result = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        quality: 0.8,
      });
      if (result.canceled || !result.assets?.[0]) return;

      setUploading(true);
      const uri = result.assets[0].uri;
      const up = await uploadImageFile(uri, 'proof');
      if ('url' in up) {
        onChange([...value, up.url]);
      } else {
        toast.error(up.error);
      }
    } catch (err: any) {
      toast.error(err?.message || 'Upload failed. Please try again.');
    } finally {
      setUploading(false);
    }
  };

  const tileBase = {
    width: TILE,
    height: TILE,
    borderRadius: radius.md,
    overflow: 'hidden' as const,
  };

  return (
    <View style={{ gap: spacing.sm }}>
      {label ? (
        <Text variant="caption" color="textSecondary" weight="600">
          {label}
        </Text>
      ) : null}

      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
        {value.map((url, idx) => (
          <View key={`${url}-${idx}`} style={tileBase}>
            <Image source={{ uri: url }} style={{ width: '100%', height: '100%' }} />
            <PressableScale
              onPress={() => remove(idx)}
              accessibilityRole="button"
              accessibilityLabel="Remove image"
              hitSlop={6}
              style={{
                position: 'absolute',
                top: spacing.xs,
                insetInlineEnd: spacing.xs,
                width: 22,
                height: 22,
                borderRadius: radius.pill,
                alignItems: 'center',
                justifyContent: 'center',
                backgroundColor: colors.background,
                borderWidth: 1,
                borderColor: colors.border,
              }}
            >
              <Icon name="close" size={14} color={colors.textPrimary} />
            </PressableScale>
          </View>
        ))}

        {canAdd ? (
          <PressableScale
            onPress={add}
            disabled={uploading}
            accessibilityRole="button"
            accessibilityLabel={uploading ? 'Uploading image' : 'Add image'}
            style={[
              tileBase,
              {
                alignItems: 'center',
                justifyContent: 'center',
                backgroundColor: colors.surfaceAlt,
                borderWidth: 1,
                borderColor: colors.hairline,
                borderStyle: 'dashed',
                opacity: uploading ? 0.7 : 1,
              },
            ]}
          >
            {uploading ? (
              <Spinner size="small" />
            ) : (
              <Icon name="plus" size={22} color={colors.textSecondary} />
            )}
          </PressableScale>
        ) : null}
      </View>

      <Text variant="caption" color="textMuted">
        {value.length}/{max}
      </Text>
    </View>
  );
};
