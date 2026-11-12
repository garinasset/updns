#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
	echo "Usage: $0 SOURCE_DIR SDK_DIR all|backend" >&2
	exit 2
fi

repo=$1
sdk=$2
mode=$3
case "$mode" in all|backend) ;; *) exit 2 ;; esac
version=$(sed -n 's/^PKG_VERSION:=//p' "$repo/openwrt/updns/Makefile")
case "$version" in ''|*[!A-Za-z0-9._+-]*) exit 1 ;; esac

cd "$sdk"
./scripts/feeds update base packages luci
test -d feeds/packages/lang/golang
test -f feeds/luci/luci.mk
./scripts/feeds install -p base ca-certificates nftables
mkdir -p dl feeds/packages/net feeds/luci/applications
tar -czf "dl/updns-$version.tar.gz" -C "$repo" \
	--transform="s,^,updns-$version/," \
	go.mod go.sum cmd config server resolver rules cache nftset logging
test ! -e feeds/packages/net/updns
test ! -e feeds/luci/applications/luci-app-updns
cp -R "$repo/openwrt/updns" feeds/packages/net/updns
cp -R "$repo/openwrt/luci-app-updns" feeds/luci/applications/luci-app-updns
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
make CONFIG_AUTOREMOVE= package/updns/compile
if [ "$mode" = all ]; then
	make CONFIG_AUTOREMOVE= package/feeds/luci/luci-app-updns/compile
fi
