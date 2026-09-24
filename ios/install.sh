#!/bin/sh
# Builds the LSM iOS app and installs it (SPEC §10.1: a developer app
# side-loaded onto a personal iPhone; no distribution, no TestFlight).
#
#   ios/install.sh            the first connected, paired iPhone
#   DEVICE=<name|udid> …      a specific device
#   DEVICE=sim …              the Simulator (SIMULATOR names the model)
#
# Device builds sign with automatic provisioning under IOS_TEAM, which on
# first run registers IOS_BUNDLE_ID with that Apple developer account.
set -eu
cd "$(dirname "$0")"

TEAM=${IOS_TEAM:-2C564TQ2FY}
BUNDLE=${IOS_BUNDLE_ID:-com.jasonfigge.lsm}
SIMULATOR=${SIMULATOR:-iPhone 17}
DEVICE=${DEVICE:-}
OUT=build

if [ "$DEVICE" = sim ]; then
    xcodebuild -quiet -project LSM.xcodeproj -scheme LSM -configuration Debug \
        -destination "platform=iOS Simulator,name=$SIMULATOR" -derivedDataPath "$OUT" \
        PRODUCT_BUNDLE_IDENTIFIER="$BUNDLE" build
    xcrun simctl boot "$SIMULATOR" 2>/dev/null || true
    open -a Simulator
    xcrun simctl install "$SIMULATOR" "$OUT/Build/Products/Debug-iphonesimulator/LSM.app"
    xcrun simctl launch "$SIMULATOR" "$BUNDLE"
    exit 0
fi

if [ -z "$DEVICE" ]; then
    list=$(mktemp)
    trap 'rm -f "$list"' EXIT
    xcrun devicectl list devices --json-output "$list" >/dev/null
    DEVICE=$(/usr/bin/python3 -c '
import json, sys
devs = json.load(open(sys.argv[1]))["result"]["devices"]
for d in devs:
    hw, conn = d.get("hardwareProperties", {}), d.get("connectionProperties", {})
    if hw.get("deviceType") == "iPhone" and conn.get("pairingState") == "paired" and conn.get("tunnelState") != "unavailable":
        print(d["identifier"]); break
' "$list")
    if [ -z "$DEVICE" ]; then
        echo "No paired iPhone is available. Connect and unlock it, or use DEVICE=sim." >&2
        exit 1
    fi
fi

xcodebuild -quiet -project LSM.xcodeproj -scheme LSM -configuration Debug \
    -destination 'generic/platform=iOS' -derivedDataPath "$OUT" -allowProvisioningUpdates \
    DEVELOPMENT_TEAM="$TEAM" PRODUCT_BUNDLE_IDENTIFIER="$BUNDLE" build
xcrun devicectl device install app --device "$DEVICE" "$OUT/Build/Products/Debug-iphoneos/LSM.app"
xcrun devicectl device process launch --device "$DEVICE" "$BUNDLE" || \
    echo "Installed. If it will not open, trust the developer in Settings › General › VPN & Device Management."
