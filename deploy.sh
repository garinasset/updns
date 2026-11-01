#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
	echo "Usage: $0 /path/to/updns.apk /path/to/luci-app-updns.apk /path/to/luci-i18n-updns-zh-cn.apk user@router" >&2
	exit 2
fi

updns_apk=$1
luci_apk=$2
i18n_apk=$3
target=$4

for package in "$updns_apk" "$luci_apk" "$i18n_apk"; do
	if [ ! -f "$package" ]; then
		echo "APK not found: $package" >&2
		exit 1
	fi
done

updns_name=$(basename "$updns_apk")
luci_name=$(basename "$luci_apk")
i18n_name=$(basename "$i18n_apk")

for package_name in "$updns_name" "$luci_name" "$i18n_name"; do
	case "$package_name" in
		*[!A-Za-z0-9._+-]*)
			echo "Unsupported APK filename: $package_name" >&2
			exit 1
			;;
	esac
done

ssh "$target" "cat > '/tmp/$updns_name'" < "$updns_apk"
ssh "$target" "cat > '/tmp/$luci_name'" < "$luci_apk"
ssh "$target" "cat > '/tmp/$i18n_name'" < "$i18n_apk"
ssh "$target" "apk add --allow-untrusted '/tmp/$updns_name' '/tmp/$luci_name' '/tmp/$i18n_name'"
