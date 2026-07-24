// Renderer shared by the Privacy Policy and Terms of Service screens. Apple
// expects the policy to be readable inside the app, not only behind a link, so
// these render from the bundled text in constants/legal rather than a WebView —
// they work offline and can't 404.
import React from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack } from 'expo-router';
import { useTheme } from '@/theme';
import { Divider, Text } from '@/components/ui';
import type { LegalDocument } from '@/constants/legal';

interface Props {
  document: LegalDocument;
}

export default function LegalDocumentScreen({ document }: Props) {
  const { colors, spacing } = useTheme();

  return (
    <>
      <Stack.Screen options={{ headerShown: true, title: document.title }} />
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView
          contentContainerStyle={{ padding: spacing.xl, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
        >
          <Text variant="title1" weight="700">
            {document.title}
          </Text>
          <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
            Version {document.version} · Effective {document.effectiveDate}
          </Text>

          <View style={{ marginTop: spacing.lg, gap: spacing.md }}>
            {document.intro.map((paragraph) => (
              <Text key={paragraph.slice(0, 40)} variant="bodySm" color="textSecondary">
                {paragraph}
              </Text>
            ))}
          </View>

          <Divider spacing="lg" />

          {document.sections.map((section) => (
            <View key={section.heading} style={{ marginBottom: spacing.xl }}>
              <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.sm }}>
                {section.heading}
              </Text>

              {section.body?.map((paragraph) => (
                <Text
                  key={paragraph.slice(0, 40)}
                  variant="bodySm"
                  color="textSecondary"
                  style={{ marginBottom: spacing.sm }}
                >
                  {paragraph}
                </Text>
              ))}

              {section.bullets?.map((bullet) => (
                <View
                  key={bullet.slice(0, 40)}
                  style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.sm }}
                >
                  <Text variant="bodySm" color="accent">
                    •
                  </Text>
                  <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                    {bullet}
                  </Text>
                </View>
              ))}
            </View>
          ))}

          <Divider spacing="md" />
          <Text variant="caption" color="textMuted">
            Also published at {document.url}
          </Text>
        </ScrollView>
      </SafeAreaView>
    </>
  );
}
