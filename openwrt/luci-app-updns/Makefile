include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-updns
PKG_VERSION:=1.0.25
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
PKG_PO_VERSION:=$(PKG_VERSION)

LUCI_TITLE:=UpDNS DNS service
LUCI_DEPENDS:=+luci-base
LUCI_EXTRA_DEPENDS:=nftables-json (>=1.1.6-r1)
LUCI_PKGARCH:=all

include ../../luci.mk

# call BuildPackage - OpenWrt buildroot signature
