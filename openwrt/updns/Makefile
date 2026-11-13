include $(TOPDIR)/rules.mk

PKG_NAME:=updns
PKG_VERSION:=1.0.0
PKG_RELEASE:=62

PKG_SOURCE:=$(PKG_NAME)-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=file://$(TOPDIR)/dl
PKG_HASH:=skip
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

PKG_MAINTAINER:=updns Community
PKG_LICENSE:=Apache-2.0
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1

GO_PKG:=github.com/garinasset/updns
GO_PKG_BUILD_PKG:=github.com/garinasset/updns/cmd/updns

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

ifneq ($(filter $(ARCH_PACKAGES),mips_24kc mipsel_24kc mipsel_24kc_24kf mipsel_74kc),)
  GO_CFLAGS_TO_REMOVE+=-mips16 -minterlink-mips16
endif

define Package/updns
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=DNS
  TITLE:=updns upstream DNS server
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle +nftables
endef

# 描述 - 软件包
define Package/updns/description
  Turnout DNS (application name: updns) is a local DNS server for OpenWrt. Compared with dnsmasq,
  it offers more flexible DNS routing and nftables set integration, along with DoT/DoH upstream support,
  ad blocking, and DNS auditing.
endef

define Package/updns/conffiles
/etc/config/updns
endef

define Package/updns/postinst
#!/bin/sh
if [ -n "$${IPKG_INSTROOT}" ]; then
	/bin/sh "$${IPKG_INSTROOT}/etc/rc.common" "$${IPKG_INSTROOT}/etc/init.d/updns" disable || exit 1
else
	enabled="$$(uci -q get updns.general.enabled 2>/dev/null)" || enabled=0
	case "$$enabled" in
		1)
			/etc/init.d/updns enable || exit 1
			/etc/init.d/updns restart || exit 1
			;;
		0|"")
			/etc/init.d/updns disable || exit 1
			/etc/init.d/updns stop || exit 1
			;;
		*)
			echo "Invalid updns.general.enabled value: $$enabled" >&2
			exit 1
			;;
	esac
fi
exit 0
endef

define Build/Prepare
	$(call Build/Prepare/Default)
	$(call GoPackage/Build/Prepare)
endef

define Build/Compile
	$(call GoPackage/Build/Compile)
endef

define Package/updns/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/updns $(1)/usr/bin/updns
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./root/etc/init.d/updns $(1)/etc/init.d/updns
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_BIN) ./root/usr/libexec/updns-uci-reload $(1)/usr/libexec/updns-uci-reload
	$(INSTALL_DIR) $(1)/usr/share/ucitrack
	$(INSTALL_DATA) ./root/usr/share/ucitrack/updns.json $(1)/usr/share/ucitrack/updns.json
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./root/etc/config/updns $(1)/etc/config/updns
	$(INSTALL_DIR) $(1)/etc/updns/domainlists
endef

$(eval $(call GoBinPackage,updns))
$(eval $(call BuildPackage,updns))
