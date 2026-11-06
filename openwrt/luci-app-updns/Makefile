include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-updns
PKG_VERSION:=1.0.38
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
PKG_PO_VERSION:=$(PKG_VERSION)

LUCI_TITLE:=LuCI support for UpDNS
LUCI_DEPENDS:=+luci-base +updns
LUCI_PKGARCH:=all

include ../../luci.mk

# call BuildPackage - OpenWrt buildroot signature
