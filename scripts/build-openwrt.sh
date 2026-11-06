#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "Usage: $0 /path/to/openwrt-sdk" >&2
	exit 2
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sdk=$(CDPATH= cd -- "$1" && pwd)
source_archive="$sdk/dl/updns-1.0.0.tar.gz"
package_feed="$sdk/feeds/packages/net/updns"
luci_feed="$sdk/feeds/luci/applications/luci-app-updns"
managed_feed_marker="$sdk/.updns-build-feed"

if [ ! -f "$sdk/Makefile" ] || [ ! -x "$sdk/scripts/feeds" ]; then
	echo "The supplied directory is not an OpenWrt SDK root: $sdk" >&2
	exit 1
fi

cd "$sdk"
if [ ! -f feeds/base.index ] || [ ! -f feeds/packages.index ] || [ ! -f feeds/luci.index ]; then
	./scripts/feeds update base packages luci
fi

# Install package definitions required by UpDNS before indexing its feed entry.
./scripts/feeds install -p base ca-certificates nftables

mkdir -p "$sdk/dl" "$sdk/feeds/packages/net" "$sdk/feeds/luci/applications"
tar -czf "$source_archive" -C "$repo" \
	--transform='s,^,updns-1.0.0/,' \
	go.mod go.sum cmd config server resolver rules cache nftset logging

if [ -e "$package_feed" ] || [ -e "$luci_feed" ]; then
	if [ ! -f "$managed_feed_marker" ] || [ "$(cat "$managed_feed_marker")" != "updns-build-feed-v1" ]; then
		echo "Refusing to overwrite package directories not managed by this build script." >&2
		exit 1
	fi
	rm -rf -- "$package_feed" "$luci_feed"
fi

cp -R "$repo/openwrt/updns" "$package_feed"
cp -R "$repo/openwrt/luci-app-updns" "$luci_feed"
printf '%s\n' "updns-build-feed-v1" > "$managed_feed_marker"

./scripts/feeds update -i base packages luci
./scripts/feeds install -p base lua liblua libubox libubus rpcd rpcd-mod-file rpcd-mod-ucode ucode libucode ucode-mod-fs ucode-mod-log ucode-mod-math ucode-mod-ubus ucode-mod-uci ucode-mod-html
./scripts/feeds install -p packages golang updns cgi-io
./scripts/feeds install -p luci luci-base luci-app-updns
cat >> .config <<'EOF'
CONFIG_PACKAGE_updns=m
CONFIG_PACKAGE_luci-app-updns=m
CONFIG_LUCI_LANG_zh_Hans=y
EOF
make defconfig
# Keep any remaining build trees between package targets.
make CONFIG_AUTOREMOVE= package/updns/compile package/feeds/luci/luci-app-updns/compile

echo
echo "Built packages:"
find "$sdk/bin/packages/x86_64" -type f \( -name 'updns-*.apk' -o -name 'luci-app-updns-*.apk' -o -name 'luci-i18n-updns-zh-cn-*.apk' \) -print
