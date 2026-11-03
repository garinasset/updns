# UpDNS Guide

[简体中文](./GUIDE.md) | **English**

[← Back to README](../README.en.md) · [Repository](https://github.com/garinasset/updns)

This guide is for UpDNS users and maintainers. It describes product behavior, configuration, the build process, and how the LuCI page maps to the backend configuration.

## Product Overview

UpDNS is a DNS service for OpenWrt. It accepts queries over UDP/TCP, selects upstream DNS servers according to domain-list rules, and can write resolved addresses into specified nftables sets. When no rule matches, the default upstreams are used; a fallback rule can also be configured.

Supported upstream protocols: UDP, TCP, DNS over TLS (DoT), and DNS over HTTPS (DoH). UpDNS can be used standalone, or as the upstream of a DNS forwarder such as dnsmasq.

Main capabilities:

- **Rules**: match by domain list; query multiple upstreams sequentially or concurrently; address-family filtering, NXDOMAIN responses, and Zero-IP Blocking (returning 0.0.0.0 / ::).
- **Cache**: positive entries are managed by the record TTL; negative entries use the RFC 2308 TTL from the response SOA. Cache hits return a decremented TTL. Capacity limit and active preload are supported.
- **Active preload** (enabled by default): a background loop uses a 100 ms scan interval to re-resolve entries that have passed 75% of their TTL but have not yet expired (including rule matching and nftables writes). Each batch refreshes at most 8 entries concurrently and finishes before the next batch starts. Successful replacements retain the entry's hit count, including hits received during refresh. Results are discarded if the scanned entry has since been removed or replaced, avoiding stale overwrites or restoring cleared entries. Preload does not distinguish whether an entry is still being queried, and does not serve expired data when a refresh fails; entries that fail to refresh simply expire naturally. The 100 ms interval is not a per-entry completion deadline; heavy load can still cause refresh windows to be missed.
- **nftables**: add resolved IPv4 and IPv6 addresses to the sets specified by a rule; optionally also add the endpoint addresses of the upstreams referenced by the rule.
- **LuCI**: manage the service, upstreams, domain lists, cache, logging, and rules from a web UI.
- **CLI**: `updns <domain>` shows the query result, matched rule, cache/upstream path, and nftables set status.

## Configuration and Rules

OpenWrt configuration uses the UCI format:

- Main config: `/etc/config/updns`
- Domain lists: usually under `/etc/updns/domainlists/`, one domain per line
- Default port: `153`, listening on both UDP and TCP

The package ships with five upstreams by default: Tencent-UDP, Google-DoH, Cloudflare-DoH, Tencent-DoH, and AdGuard-DoH. When the local resolver file is enabled, the nameservers in that file are also used as runtime UDP upstreams; the default file is `/tmp/resolv.conf.d/resolv.conf.auto`.

Upstream address formats:

| Protocol | Example | Default port |
|---|---|---:|
| UDP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| TCP | `1.1.1.1` or `1.1.1.1:5353` | 53 |
| DoT | `dns.google` or `dns.google:853` | 853 |
| DoH | `https://dns.google/dns-query` | 443 |

A DoH address must be a full HTTPS URL. For an IPv6 address with a port, use brackets, e.g. `[2001:db8::1]:5353`.

### Domain Lists

A list entry consists of a unique name and a file path; rules reference the file through the list name. Matching is case-insensitive; blank lines and lines starting with `#` are ignored.

A plain domain matches the exact domain and its subdomains: `example.com` matches `example.com` and `www.example.com`, but not `superexample.com`. A leading `*` changes this: `*example.com` matches `example.com`, `www.example.com`, and also `superexample.com`.

### Rule Behavior

Rules are evaluated in order and evaluation stops at the first applicable match. A rule may reference multiple domain lists; matching any one of them satisfies the domain condition. LuCI supports drag-and-drop ordering of regular rules; selecting "Do Not Match (Fallback Rule)" in the rule's "If the Domain Matches" drop-down (`domain_match='0'`) makes it a fallback rule. There can be at most one fallback rule and it always runs after the regular rules.

| Action | Behavior |
|---|---|
| `ipv4+ipv6` | Return both IPv4 and IPv6 results |
| `ipv4only` | Return IPv4 results only |
| `ipv6only` | Return IPv6 results only |
| `nxdomain` | Return NXDOMAIN directly, without querying upstreams; the response is cached |
| `zeroip` | Zero-IP Blocking: return `0.0.0.0` for A queries and `::` for AAAA queries, without querying upstreams; the response is cached |

Responses of `nxdomain` and `zeroip` are cached with a fixed TTL of 60 seconds and are not written to nftables sets.

A rule (except `nxdomain` and `zeroip`) can select one or more upstreams and specify separate nftables sets for IPv4/IPv6. With concurrent query enabled, the selected upstreams are queried concurrently and the first valid answer is used; otherwise they are queried one by one in configured order until one succeeds. If no rule matches and there is no fallback rule, the default upstreams are used; when the local resolver is enabled, its upstreams come before the other default upstreams.

Both modes apply the same final-answer criteria:

- The response must have the response flag set and match the query ID, opcode, question name (case-insensitive), type, and class. Only `NOERROR` and `NXDOMAIN` are accepted; error codes such as `FORMERR`, `SERVFAIL`, and `REFUSED` cannot win. Error replies that omit Question still report their actual RCODE and question count rather than only a missing-question diagnostic.
- A positive response must contain the requested record type at the queried name or the end of its CNAME chain. A strict negative response (NODATA / NXDOMAIN) must include an SOA covering the terminal name. When an upstream returns an incomplete CNAME chain, that upstream branch queries the terminal name for the original record type using the same upstream, original rule, timeout budget, and cancellation signal. The combined chain and terminal address or SOA-backed negative result can win normally. Chains are limited to 16 total CNAME hops, including across responses, and loops are rejected; target names do not trigger rule re-matching.
- Compatibility fallback: a matching, non-truncated negative response with an empty Authority section (no SOA or referral) may be retained as an uncacheable candidate, not an immediate winner. `NOERROR` requires an explicitly queried chain terminal with an empty Answer. `NXDOMAIN` already denies the terminal and may have an empty Answer or only a complete CNAME chain rooted at the original query name; an already-denied terminal does not need another query. Other upstreams are awaited or tried. If none provides a strict final answer before all finish or the overall deadline expires, the first candidate is returned with its original RCODE and CNAME chain. The entire result is not cached and does not replace or extend an existing entry; neither CNAME TTLs nor synthesized SOAs supply a negative-cache lifetime. Explicit cancellation or service shutdown never becomes fallback success. Referrals, invalid SOAs, negative answers containing addresses or unrelated/conflicting CNAMEs, error codes, mismatched questions, failed completion, excessive hops, and loops are not candidates. Fallback can wait for other upstream timeouts, and subsequent requests must query again; debug logs report fallback selection and its actual RCODE.
- A UDP response with `TC=1` triggers a TCP retry to the same upstream address and port, sharing the upstream's timeout budget. A failed or still-truncated retry cannot win. Truncated TCP, DoT, and DoH responses are also rejected.
- Once a concurrent query selects a final answer, it cancels the remaining requests: in-flight UDP/TCP/DoT connections are closed and DoH HTTP requests are canceled. Only the selected result enters business processing, and compatibility fallback is never cached; requests already sent to remote servers cannot be retracted. If no upstream supplies a valid final answer or compatibility candidate, existing type-specific failure handling applies (SERVFAIL for ordinary queries; existing failure suppression for types such as HTTPS/SVCB). Error responses are not treated as successful answers.

For accepted NXDOMAIN / NODATA responses, the SOA record's own TTL is normalized to `min(SOA TTL, SOA.MINIMUM)` before the first downstream reply and cache insertion. Cache hits further decrement it by elapsed storage time; reads never reset the TTL. A shorter CNAME chain TTL may expire the whole cached response earlier, but SOA.MINIMUM is unchanged. Positive responses and direct SOA record queries are not subject to this negative-cache normalization.

A failed preload or an uncacheable fallback does not invalidate a still-live cached negative answer: it neither removes nor extends the old entry, so a background refresh warning can coexist with a subsequent CLI NXDOMAIN cache hit. The old entry expires on its original schedule; only a newly obtained cacheable valid result updates its lifetime.

A valid answer need not be cacheable: final answers with TTL 0 can be returned but are not cached. Negative caching uses `min(SOA TTL, SOA.MINIMUM)`; when a CNAME chain and a terminal negative result are cached together, the CNAME TTL also bounds their lifetime. Concurrent selection favors speed, while sequential selection favors configured priority, so differences in upstream TTLs or results can still produce different cache sizes. Each preloaded entry follows the same upstream-selection mode. At most 8 entries refresh at once; with concurrent upstream queries enabled, instantaneous upstream request counts also scale with the number of upstreams selected per entry. Stopping the service cancels in-flight DNS queries and waits for the preload loop to exit.

A rule can also add the IP addresses of the endpoints of its referenced upstreams to the corresponding nftables sets. Hostnames (including DoH URL hostnames) are resolved to IPs and refreshed periodically; new addresses are added to the sets, and old addresses are not automatically removed from shared sets.

### Basic UCI Example

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option concurrent_query '0'
    option cache_enabled '1'
    option cache_size '4096'
    option active_preload '1'
    option log_query '0'
    option log_level 'info'

config domainlist 'example'
    option name 'example-domains'
    option file '/etc/updns/domainlists/example.txt'

config rule 'example_rule'
    option name 'Example'
    option domain_match '1'
    list domain_lists 'example-domains'
    list upstreams 'Google-DoH'
    option action 'ipv4+ipv6'
    list ipv4_nftsets 'inet/fw4/example_v4'
    list ipv6_nftsets 'inet/fw4/example_v6'
    option add_upstream_ips '0'
```

See [`openwrt/updns/root/etc/config/updns`](../openwrt/updns/root/etc/config/updns) for the full default configuration. Log levels are `debug`, `info`, `warn`, or `error`; per-query DNS logging additionally requires `log_query`.

## LuCI Product and Frontend Reference

The entry is **Services → UpDNS**, at route `/admin/services/updns`. The page is divided into Status, Settings & Port, Upstream DNS, Domain Lists, Cache, Logging, and Rules areas.

### Page and Configuration Model

| Page area | UCI section / fields | Product behavior |
|---|---|---|
| Status | Service status, cache status file | Shows service status and cache entry count, refreshed periodically |
| Settings & Port | `updns.general`: `enabled`, `port`, `use_local_resolv`, `local_resolv_file`, `concurrent_query` | Enable/disable service, listen port, local resolver, upstream query mode |
| Upstream DNS | `updns.upstream`: `name`, `address`, `protocol`, `timeout` | Manage UDP/TCP/DoT/DoH upstreams |
| Domain Lists | `updns.domainlist`: `name`, `file` | Upload or manage list files referenced by rules |
| Cache | `updns.general`: `cache_enabled`, `cache_size`, `active_preload` | Control cache, capacity, and active preload |
| Logging | `updns.general`: `log_query`, `log_level` | Control query logging and log level |
| Rules | `updns.rule`: `name`, `domain_match`, `domain_lists`, `upstreams`, `action`, `ipv4_nftsets`, `ipv6_nftsets`, `add_upstream_ips` | Set match conditions, upstreams, response type, and nftables handling |

On a fresh install `enabled=0`, so the service does not start automatically. Defaults: port `153`, local resolver used, sequential queries, cache enabled with capacity `4096`, active preload enabled, query logging off.

The "Block" drop-down in the rule editor (No Blocking / NXDOMAIN Response / Zero-IP Response) is a UI control for the `action` field, not a separate UCI field: NXDOMAIN Response maps to `action='nxdomain'` and Zero-IP Response to `action='zeroip'`. Selecting either hides and clears the upstream, nftables set, and "And Return" options.

A rule that selects "Do Not Match (Fallback Rule)" (`domain_match='0'`) is a fallback rule. Only one is allowed; it runs after no other rule has matched, and the LuCI page automatically keeps it after the regular rules.

### Frontend Files and Maintenance Notes

| Content | Source location |
|---|---|
| Page view, forms, and status display | [`config.js`](../openwrt/luci-app-updns/root/www/luci-static/resources/view/updns/config.js) |
| Menu route | [`menu.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/luci/menu.d/luci-app-updns.json) |
| rpcd/ubus permissions | [`acl.d/luci-app-updns.json`](../openwrt/luci-app-updns/root/usr/share/rpcd/acl.d/luci-app-updns.json) |
| Simplified Chinese translation | [`updns.po`](../openwrt/luci-app-updns/po/zh_Hans/updns.po) |
| Backend default UCI config | [`updns`](../openwrt/updns/root/etc/config/updns) |
| Package metadata and dependencies | [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile), [`openwrt/updns/Makefile`](../openwrt/updns/Makefile) |

The page reads and saves configuration through UCI; local resolver entries appear as auto-generated `local-resolv-N` upstreams. Service status comes from rpcd `service.list`, the cache entry count from `/tmp/updns_cache_size`, and nftables sets from the page calling `nft -j list sets`. Domain lists are read and written through the rpcd file interface.

When changing the page, check the related UCI fields, the rpcd ACL, and backend behavior together. The ACL grants explicit file paths; when adding a new status file, file read/write path, or system command, update the ACL as well. Visible text uses `_()` and the PO translation must be kept in sync. If you change form sections or tab layout, review the mapping and ordering logic in the page.

## dnsmasq Integration

To use UpDNS as the dnsmasq upstream, add the local forwarding address:

```sh
uci add_list dhcp.@dnsmasq[0].server='127.0.0.1#153'
uci set dhcp.@dnsmasq[0].cachesize='0'
uci commit dhcp
/etc/init.d/dnsmasq restart
```

Enabling the cache only in UpDNS is recommended, to avoid the extra state-debugging effort and update delay of two cache layers. The two layers expire independently according to their own TTL and cache policy; TTLs are not necessarily extended twice. If you want dnsmasq to use only UpDNS, set `noresolv='1'`. UpDNS can also serve DNS on its own; before switching to port `53`, make sure that port is not occupied by dnsmasq or another program.

## CLI

```text
root@OpenWrt:~# updns google.com

╭─ updns ──────────────────────────────────────╮
│
│  DOMAIN
│  ──────────────────────────────────────────
│  Domain       : google.com.
│  Query Type   : A / AAAA
│  Query Path   : Cache Hit
│
│  DNS RESULT
│  ──────────────────────────────────────────
│  Matched Rule : US Access
│  Upstream     : Google-DoH
│  Action       : IPv4 + IPv6
│  Response     : NOERROR
│
│  NFT SETS
│  ──────────────────────────────────────────
│  IPv4         : inet/fw4/us4
│                 ✓ Contains resolved IPv4
│  IPv6         : inet/fw4/us6
│                 ✓ Contains resolved IPv6
│
│  ANSWERS
│  ──────────────────────────────────────────
│  • 142.251.219.142
│  • 2607:f8b0:4005:818::200e
│
╰──────────────────────────────────────────────╯
```

When the service is running, the CLI queries through the local control interface, using the service's rules and cache and showing nftables set information; when the service is not running, it queries independently without the service cache. By default it performs A and AAAA queries separately.

When a query fails the full report is still printed: `Response` shows `ERROR` and an `ERRORS` section gives one normalized short reason per query type (for example `Upstream timeout`, `Upstream connection refused`, `Upstream host unresolvable`, `Network not ready`, `No upstream configured`, `All upstreams failed`), and the command exits non-zero. If only one of A/AAAA fails, the other result is shown normally and the exit code is 0 as long as one succeeds. The frame width adapts to the terminal (minimum 48, maximum 80 columns).

## Service Management

The service is managed by OpenWrt init/procd. `general.enabled` defaults to `0`; saving and applying configuration triggers an init script reload, which starts or stops the service according to that field.

```sh
/etc/init.d/updns enable
/etc/init.d/updns disable
/etc/init.d/updns start
/etc/init.d/updns stop
/etc/init.d/updns restart
/etc/init.d/updns reload
/etc/init.d/updns status
```

- `enable` enables start at boot.
- `disable` disables start at boot.
- `start` starts the service process; if it is already running, it returns directly.
- `restart` is equivalent to `stop` (stop the process and clear nftables sets) followed by `start`; the cache is rebuilt when the process restarts.
- `reload`
  - with `general.enabled=1`, goes through the restart path;
  - with `general.enabled=0`, goes through the stop path.
- `stop` stops the service process (the cache is released when the process exits) and, after the process has stopped, clears the nftables sets referenced by the current configuration.

Saving and applying configuration in LuCI triggers `reload`.

Boot start is managed by init and is on by default, but `general.enabled` defaults to `0`, so the service process is not started. Once `general.enabled` is set to `1`, the service process is also started at boot if boot start is enabled. The existing configuration is preserved on upgrade and is not overwritten.

To enable or disable the service, use LuCI, or keep UCI `general.enabled` in sync.

## Build, Install, and Test

### Local Development

Requires Go 1.21 or later. Run on a macOS/Linux development machine; locally built binaries are for development and checks and must not be installed directly on the router.

```sh
make build
make test
make lint
```

The binary is output to `bin/updns`. `make test` uses the race detector; `make lint` runs gofmt and go vet.

### OpenWrt APK

Use an SDK matching the target OpenWrt version; the build script in this repository produces APKs with an x86_64 SDK. Put the source and SDK on a Linux x86_64 build host and run from the project root:

```sh
./scripts/build-openwrt.sh /path/to/openwrt-sdk
```

The script syncs feeds, builds `updns` and `luci-app-updns`, and produces the Simplified Chinese translation package per the LuCI build rules. To install a rebuild as a same-version update, bump the package version first:

- Backend: `PKG_RELEASE` in [`openwrt/updns/Makefile`](../openwrt/updns/Makefile)
- LuCI and translation: `PKG_VERSION` or `PKG_RELEASE` in [`openwrt/luci-app-updns/Makefile`](../openwrt/luci-app-updns/Makefile)

Generated APKs are under `bin/packages/x86_64/` in the SDK, in the `packages/` and `luci/` subdirectories. Exact filenames depend on the SDK output.

The APKs are not signed with a trusted key. Copy the three packages to the device and install:

```sh
apk add --allow-untrusted \
    /tmp/updns-<version>.apk \
    /tmp/luci-app-updns-<version>.apk \
    /tmp/luci-i18n-updns-zh-cn-<version>.apk
```

You can also upload and install with the repository's [`deploy.sh`](../deploy.sh):

```sh
./deploy.sh ./artifacts/updns-<version>.apk \
    ./artifacts/luci-app-updns-<version>.apk \
    ./artifacts/luci-i18n-updns-zh-cn-<version>.apk \
    root@router-host
```

For test/development environments, see [`scripts/build-openwrt.sh`](../scripts/build-openwrt.sh) and the deploy script; for official releases, use an SDK matching the target firmware and verify on an OpenWrt device.

## Troubleshooting

- **Service not running**: check `general.enabled` in `/etc/config/updns`, view `logread -e updns`, and confirm the listening port is not in use.
- **Upstream query failures**: check the upstream address, protocol, network connectivity, and the device CA certificates; set `log_level` to `debug` for more information.
- **Cache not effective**: confirm `cache_enabled=1`; NXDOMAIN and Zero-IP rule responses do not query upstreams but are cached.
- **nftables update failures**: confirm `nftables-json` is installed, the target set exists, and the set family matches the address family.
- **LuCI cannot read data**: verify the rpcd ACL includes the required UCI, file, or command permissions, and reload the rpcd/LuCI services.

UpDNS's regular logs are collected by procd and can be viewed with `logread`. Per-query DNS logging also requires `log_query`.

With "Log Queries" enabled and `add_subnet` configured in dnsmasq, the log can record `client` and `ecs`.

## Project Structure

| Area | Location | Purpose |
|---|---|---|
| Entry point and CLI | `cmd/updns/` | Service startup, query command, and output |
| Config, rules, upstreams, cache | `config/`, `rules/`, `resolver/`, `cache/` | UCI parsing, matching, DNS queries, and caching |
| DNS server and control interface | `server/` | Request handling, control interface, status, and cache logic |
| nftables and logging | `nftset/`, `logging/` | Set operations and logging |
| OpenWrt packages and LuCI | `openwrt/` | init script, default UCI config, UI, ACL, and translation |
| Build and deploy | `scripts/build-openwrt.sh`, `deploy.sh`, `Makefile` | Local build, SDK packaging, and device install |

## License

[Apache License 2.0](../LICENSE)
