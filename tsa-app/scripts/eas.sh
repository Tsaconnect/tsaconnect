#!/usr/bin/env bash
# Runs eas with THIS project's access token, never the globally logged-in account
# (which belongs to another company). The token lives in the macOS Keychain, not in
# a file. One-time setup, typed in your own terminal (it prompts for the token):
#   security add-generic-password -a "$USER" -s tsa-expo-token -w
set -euo pipefail
TOKEN=$(security find-generic-password -s tsa-expo-token -w 2>/dev/null) || {
  echo "eas.sh: no 'tsa-expo-token' in the Keychain; refusing to fall back to the global eas login." >&2
  echo "Add it with: security add-generic-password -a \"\$USER\" -s tsa-expo-token -w" >&2
  exit 1
}
EXPO_TOKEN="$TOKEN" exec eas "$@"
