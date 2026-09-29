// components/kyc/DiditSession.tsx
//
// The Didit hosted verification session, shared by first-time KYC
// (app/profile/kyc.tsx) and a verified user's name change
// (app/profile/name-change.tsx): the camera permission it needs, the WebView
// that hosts it, and the moment it has finished. Each screen gets its session
// URL from its own endpoint and asks its own endpoint what the session ended
// as — the webhook, not the redirect, is the source of truth.
//
// The WebView is granted the camera, so it may only ever show Didit: a page on
// any other origin is never loaded in it, as the page or in a frame of it
// (DIDIT_HOSTS, https only). On iOS the
// camera is granted without asking only to the host of the page on screen
// (anything else prompts). react-native-webview 13.15 has no JS permission
// hook on Android — its native client grants the camera to whatever page is
// loaded once the app holds the permission — so there the allowlist on what
// loads is the whole guard, which is why nothing but Didit is ever loaded.
import React from 'react';
import { Alert, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Camera } from 'expo-camera';
import { WebView } from 'react-native-webview';
import { useTheme } from '@/theme';

/** Ask for the camera; explain and offer Settings when refused. */
export async function requestCameraPermission(): Promise<boolean> {
  const { status } = await Camera.requestCameraPermissionsAsync();
  if (status === 'granted') return true;

  Alert.alert(
    'Camera Permission Required',
    'Camera access is needed for identity verification. Please enable it in your device settings.',
    [
      { text: 'Cancel', style: 'cancel' },
      { text: 'Open Settings', onPress: () => Linking.openSettings() },
    ],
  );
  return false;
}

/** The domain Didit's hosted flow is served from (verify.didit.me and its siblings). */
const DIDIT_HOSTS = ['didit.me'];

/** The authority of an https URL, read strictly: a plain host name and an optional port, nothing else. */
const STRICT_HTTPS_HOST = /^https:\/\/([a-z0-9.-]+)(?::\d{1,5})?(?:[/?#]|$)/i;

/**
 * An https page on a Didit host — the only pages the session WebView shows.
 *
 * Parsed with URL, and held to agreeing with a strict reading of the
 * authority: React Native's own URL is a set of regexes that does not validate
 * (it reads "https://evil.com#@verify.didit.me" as Didit's), while a browser
 * reads a backslash as a slash ("https://evil.com\@verify.didit.me" is
 * evil.com). So a backslash, whitespace, a user name or password, or any
 * disagreement between the two readings is refused.
 */
export function isDiditOrigin(raw: string): boolean {
  if (/[\\\s]/.test(raw)) return false;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  const strict = STRICT_HTTPS_HOST.exec(raw);
  const host = url.hostname.toLowerCase();
  if (url.protocol !== 'https:' || url.username || url.password || !strict || strict[1].toLowerCase() !== host) return false;
  return DIDIT_HOSTS.some((d) => host === d || host.endsWith(`.${d}`));
}

/** A frame's own blank document (about:blank, about:srcdoc) — no origin of its own to load. */
const isBlankFrame = (url: string) => url === 'about:blank' || url === 'about:srcdoc';

/**
 * Didit hands back — to our tsaconnect://kyc/callback deep link (a name change
 * adds ?flow=name_change), or with verificationSessionId + status — once the
 * hosted flow ends.
 */
export function isDiditFinished(navUrl: string): boolean {
  const url = navUrl.toLowerCase();
  return (
    url.includes('verificationsessionid=') ||
    url.includes('tsaconnect://kyc/callback') ||
    url.includes('/kyc/callback') ||
    url.includes('status=approved') ||
    url.includes('status=declined') ||
    url.includes('status=in%20review') ||
    url.includes('status=in+review')
  );
}

/**
 * Whether the session WebView may load `url`. The end of the flow is caught
 * here — never loaded, so the callback deep link is not handed to the OS (which
 * would open the first-time KYC route) — and reported through `onFinished`.
 */
export function shouldLoadInDidit(url: string, onFinished: () => void): boolean {
  if (isDiditFinished(url)) {
    onFinished();
    return false;
  }
  return isDiditOrigin(url) || url === 'about:blank';
}

/**
 * Whether a frame inside the session page may load `url`: Didit's own pages
 * and blank frames only. A frame shares the page's camera grant, so the page's
 * allowlist holds for its frames too; a frame never finishes the flow.
 */
export function shouldLoadInDiditFrame(url: string): boolean {
  return isDiditOrigin(url) || isBlankFrame(url);
}

/** The hosted session, full screen. `onFinished` runs once, when Didit hands back. */
export function DiditWebView({ url, onFinished }: { url: string; onFinished: () => void }) {
  const { colors } = useTheme();
  const finished = React.useRef(false);
  const finish = React.useCallback(() => {
    if (finished.current) return;
    finished.current = true;
    onFinished();
  }, [onFinished]);
  // A session URL that isn't Didit's is never opened.
  React.useEffect(() => {
    if (!isDiditOrigin(url)) {
      Alert.alert('Verification unavailable', 'The verification page could not be opened. Please try again.');
      finish();
    }
  }, [url, finish]);
  if (!isDiditOrigin(url)) return null;
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <WebView
        source={{ uri: url }}
        originWhitelist={['https://*', 'tsaconnect://*']}
        onShouldStartLoadWithRequest={(req) =>
          (req.isTopFrame === false ? shouldLoadInDiditFrame(req.url) : shouldLoadInDidit(req.url, finish))}
        // A redirect some platforms report only here.
        onNavigationStateChange={(nav: { url: string }) => { if (isDiditFinished(nav.url)) finish(); }}
        javaScriptEnabled
        domStorageEnabled
        mediaPlaybackRequiresUserAction={false}
        allowsInlineMediaPlayback
        // iOS: granted silently only to the page's own (Didit) host; anything else asks.
        mediaCapturePermissionGrantType="grantIfSameHostElsePrompt"
        allowsBackForwardNavigationGestures={false}
        style={{ flex: 1 }}
      />
    </SafeAreaView>
  );
}
