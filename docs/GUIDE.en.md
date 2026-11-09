# UpDNS Guide

[简体中文](./GUIDE.md) | **English**

[← Back to README](../README.en.md) · [Repository](https://github.com/garinasset/updns)

This guide is for UpDNS users and maintainers. It describes product behavior, configuration, the build process, and how the LuCI page maps to the backend configuration.

## Product Overview

UpDNS is a DNS service for OpenWrt. It accepts queries over UDP/TCP, selects upstream DNS servers according to domain-list rules, and can write resolved addresses into specified nftables sets. When no rule matches, the default upstreams are used; a fallback rule can also be configured.

Supported upstream protocols: UDP, TCP, DNS over TLS (DoT), and DNS over HTTPS (DoH). UpDNS can be used standalone, or as the upstream of a DNS forwarder such as dnsmasq.

Main capabilities:

- **Rules**: match by domain list; Dynamic Scheduling, Strict Order and Concurrent Query strategies; address-family filtering, NXDOMAIN responses, and Zero-IP Blocking (returning 0.0.0.0 / ::).
- **Local data**: resolve hosts and OpenWrt DHCP names before upstream cache or network queries, while respecting rules.
- **Cache**: positive entries are managed by the record TTL; negative entries use the RFC 2308 TTL from the response SOA. Cache hits return a decremented TTL. Capacity limit and active preload are supported.
- **Active preload** (enabled by default): every 100 ms, schedule entries that have consumed at least 80% of their TTL, including expired entries. Earliest expiration wins, with at most 8 background tasks in flight globally; scans do not wait for a batch to finish. Expired queries can share a running refresh as `Prefetch Hit`; stale DNS data is never served. Failed refreshes preserve still-valid entries, but delete expired entries without retaining hit history. See Cache below.
- **nftables**: add resolved IPv4 and IPv6 addresses to the sets specified by a rule; optionally also add the endpoint addresses of the upstreams referenced by the rule.
- **LuCI**: manage the service, upstreams, domain lists, cache, logging, and rules from a web UI.
- **CLI**: `updns <domain>` shows the query result, matched rule, cache/upstream path, and nftables set status.

## Configuration and Rules

OpenWrt configuration uses the UCI format:

- Main config: `/etc/config/updns`
- Domain lists: usually under `/etc/updns/domainlists/`, one domain per line
- Default port: `153`, listening on both UDP and TCP

The package ships with five upstreams by default: Tencent-UDP, Google-DoH, Cloudflare-DoH, Tencent-DoH, and AdGuard-DoH. When the local resolver file is enabled, the nameservers in that file are also used as runtime UDP upstreams; the default file is `/tmp/resolv.conf.d/resolv.conf.auto`.

"Default upstreams" means only the servers preconfigured in the packaged configuration file; the program has no built-in upstream DNS servers. The configuration file fully determines the upstream list and order. Upstreams can be edited or deleted without being automatically restored, even when all are deleted. A missing configuration file produces an explicit error rather than a fallback configuration. Local resolver imports are controlled independently by their enable setting.

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

Each list builds a compressed reverse trie at load time. Queries follow domain suffixes instead of scanning every pattern; rules retain configuration order and matching results are not separately cached.

Each query reuses its own-domain rule match: blocking rules answer directly, and local, cache and upstream paths do not rescan rules. Dynamic HTTPS/SVCB bindings are still checked where needed, including asynchronous resolution startup and before returning. New queries and background preloads match independently; routes are not cached across requests.

### Rule Behavior

Rules are evaluated in order and evaluation stops at the first applicable match. A rule may reference multiple domain lists; matching any one of them satisfies the domain condition. LuCI supports drag-and-drop ordering of regular rules; selecting "Do Not Match (Fallback Rule)" in the rule's "If the Domain Matches" drop-down (`domain_match='0'`) makes it a fallback rule. There can be at most one fallback rule and it always runs after the regular rules.

| Action | Behavior |
|---|---|
| `ipv4+ipv6` | Return both IPv4 and IPv6 results |
| `ipv4only` | Return IPv4 results only |
| `ipv6only` | Return IPv6 results only |
| `nxdomain` | Return NXDOMAIN directly, without querying upstreams or caching the response |
| `zeroip` | Zero-IP Blocking: return `0.0.0.0` for A queries and `::` for AAAA queries, without querying upstreams or caching the response |

Rule-generated `nxdomain` and `zeroip` answers bypass UpDNS cache reads, writes and periodic preload, and are not written to nftables sets. Existing response contents and TTL semantics are preserved (zero-IP records have TTL 60; rule NXDOMAIN has no SOA). CLI/log paths remain `NXDOMAIN RULE` or `ZERO-IP RULE`, never cache hits. Upstream NXDOMAIN responses retain the existing negative-cache behavior.

The `ipv4+ipv6`, `ipv4only`, and `ipv6only` actions filter only A/AAAA records; MX, TXT, NS, SOA, SRV, PTR, and other non-address records, as well as CNAMEs, are returned verbatim. HTTPS/SVCB records are returned, with parameters such as `alpn` and `ech` preserved.

HTTPS/SVCB handling:

- **Address hints:** `ipv4hint`/`ipv6hint` are filtered by the rule action (for example, `ipv4only` removes `ipv6hint`), and the kept hint addresses are written to the rule's nftables sets just like A/AAAA records.
- **Target binding:** The target name (TargetName, or the query name itself when ServiceMode uses `.`) is bound to the rule that produced the response. When a client then queries the target name, UpDNS uses that rule's upstreams and action, and writes the addresses to the sets of every bound rule as well as the target's own matched rule, keeping nftables routing consistent. When several rules bind the same target, upstreams and action come from the first bound rule.
- **Binding lifetime:** Bindings expire with the record TTL, at least 30 seconds. Capacity equals `cache_size`; when full, new HTTPS/SVCB records are dropped and active bindings are never evicted. After binding, target addresses are prefetched asynchronously; no synchronous follow-up query is made.
- **Limits:** If the target name itself matches an `nxdomain`/`zeroip` rule, or the chain (including CNAMEs) exceeds 16 hops, the HTTPS/SVCB record is dropped so blocking cannot be bypassed. Queries without a matching rule (default upstreams) create no binding. Error-RCODE responses are returned verbatim but create no binding, and their hints are not written to sets.

### Local resolution

UpDNS reads `/etc/hosts`, hosts files under `/tmp/hosts/`, and DHCP leases directly, supporting A, AAAA, PTR and ANY. The first dnsmasq section of `/etc/config/dhcp` supplies `domain`, `expandhosts`, `leasefile`, `addnhosts` and `hostsdir`; odhcpd's `hostsdir` is also included. Without OpenWrt DHCP configuration, the lease path defaults to `/tmp/dhcp.leases`, the domain to `lan`, and hosts short names are not expanded.

Local records override older upstream cache entries; hosts take precedence over DHCP leases only for the same name and address family. For example, IPv6 hosts records do not suppress IPv4 DHCP leases. Expired leases are not served. Queries detect file/directory/configuration updates. Local replies have TTL 0, are not DNS-cached or periodically preloaded, and are shown as `Local Answer` with source `local-hosts` or `local-dhcp` (`local-hosts+local-dhcp` for mixed-source ANY replies). Blocking, address-family filtering and nftset rules still apply. If local data cannot be read or parsed, a rate-limited warning is logged and cache/upstream resolution continues. `use_local_resolv` controls upstream nameserver import only, not local records.

### Upstream DNS Selection Strategy

`query_strategy` accepts only `0`, `1`, or `2` and defaults to `0`; the former word-based values are not supported:

- **Dynamic Scheduling (`0`)**: initially query all members of the ordered upstream group equally and use the first valid final response, recording the winner's own request duration `t`. Normally query only that winner afterward. At `min(2t, its configured timeout)`, retain the original request and concurrently add the other upstreams; the first valid completion among all in-flight requests wins. An earlier transport or invalid-response failure immediately starts the other upstreams. Only a race win updates the selection and `t`; ordinary single-upstream success does not. Added upstream durations exclude the earlier wait, but the original upstream retains its head-start advantage. There are no query-count thresholds, periodic reevaluations, or standalone probes. Faster alternatives are not discovered while the preferred upstream remains responsive; minimum latency is not guaranteed.
- **Strict Order (`1`)**: try referenced upstreams in order until a valid final response arrives.
- **Concurrent Query (`2`)**: query selected upstreams concurrently and cancel remaining requests after the first valid final response.

All three strategies support UDP/TCP/DoT/DoH and retain per-upstream timeouts, CNAME completion and cancellation. Dynamic Scheduling uses latency-triggered hedged requests. Upstream changes and explicit cache clear reset selection and latency baseline state.

Valid cache hits do not query upstreams. Expired refreshes and preloads first reuse the entry's original winning upstream without `2t` hedging. Only transport failures, configured timeouts, or invalid responses try the other current upstreams according to the selected strategy and update the cached path. Rule/service-binding changes revalidate routing rather than freezing an obsolete policy. Without a matching/fallback rule, use the complete upstream list, with local resolver upstreams first.

All strategies follow the same response and caching rules:

- **Response validity:** The response must have the response flag set and match the query ID, opcode, question name (case-insensitive), type, and class. Validity does not depend on RCODE: valid `NOERROR`, `NXDOMAIN`, `FORMERR`, `SERVFAIL`, `REFUSED`, `NOTIMP`, and other responses can all win. Error replies that omit Question still report their actual RCODE and question count for diagnostics.
- **CNAME handling:** A `NOERROR` response must contain the requested record type, or a complete CNAME chain and its terminal result. For an incomplete CNAME chain, the terminal name is queried on the same upstream with the original rule, timeout budget, and cancellation signal, without rule re-matching. At most 16 hops are allowed, and loops across responses are detected. A referral with NS delegation but no SOA is not a terminal result; an `NXDOMAIN` must not contain terminal records of the requested type.
- **Cache eligibility:** Cache eligibility is evaluated independently after a response wins and never affects the winner or what the client receives. Positive records are cached by TTL; NODATA and NXDOMAIN are negatively cached only with a valid SOA covering the terminal name. Responses without an SOA or otherwise not cacheable are still returned normally but are not cached. Error-RCODE responses are not cached, and their addresses are not written to nftables sets.
- **Concurrent query:** After the first valid final response wins, the other upstream requests are canceled immediately: in-flight UDP/TCP/DoT connections are closed and DoH HTTP requests are canceled. Requests already sent to remote servers cannot be retracted.
- **Truncation:** A UDP response with `TC=1` triggers a TCP retry to the same upstream address and port, sharing the timeout budget; a failed or still-truncated retry cannot win. Truncated TCP, DoT, and DoH responses are not used directly either.
- **Failure handling:** When no upstream yields a valid final response, ordinary queries return a locally generated `SERVFAIL`, and types such as HTTPS/SVCB keep their existing failure suppression; the specific causes are logged at warn level. A winning valid DNS response never enters failure handling.

### Cache

Keys use lowercase fully qualified domain names and query types; A and AAAA remain independent. HitCount starts at 0 and increments for each valid cache hit, including CLI hits. Initial upstream queries, refreshes, and refresh waiters do not count as hits. Every replacement inherits the current count, including hits received during network I/O. Counts saturate rather than overflow and do not decay.

No history is retained after deletion or eviction. Only replacements of still-existing entries inherit HitCount; re-admission after deletion or eviction starts at 0.

No periodic expired cleanup runs. When admission needs space, evict exactly one entry with the lowest HitCount, without prioritizing expired entries. Ties evict the entry least recently hit by a client (insertion order for entries never hit). Background refresh does not improve this priority. Hot expired entries may therefore survive while cold valid entries are evicted.

Expired entries remain available for scheduling and count inheritance, never for answering stale data. With preload disabled, expired entries can remain until capacity reclamation or a client query resolves them again. Cache size reports occupied entries, including expired ones, not only immediately usable answers.

The cache is memory-only and is not persisted across processes. An explicit clear removes cached entries and invalidates in-flight task results; stopping waits for service tasks to finish and clears the cache; restart and OpenWrt configuration reload (which takes the restart path when enabled) discard the old process cache, and the new process starts with an empty cache. Entry-version and clear-epoch markers are only for concurrency control within one process: they prevent old tasks from deleting or overwriting newer entries or reinserting evicted or cleared entries. They are not cache-data versions and are not used to persist or restore the cache. Clearing removes references held by the cache structures, but in-flight tasks that have not finished may temporarily retain related objects; those objects become eligible for Go garbage collection only after all references are gone. Garbage collection does not guarantee that heap memory is immediately returned to the operating system, so clearing while the process remains alive does not guarantee an immediate drop in process memory usage. When the service process exits, its memory is reclaimed by the operating system.

#### Preload scheduling and shared queries

Every 100 ms, entries with at most 20% of their original lifetime remaining become pending, including expired entries. Pending excludes in-flight tasks. Select the earliest original expiration first and fill free slots up to `max(16, min(ceil(cache_size / 128), 64))` global background tasks, based on the configured cache capacity. The default capacity of 4096 gives a limit of 32. Slow tasks do not block later scans. Preload does not filter entries by HitCount or recent demand.

- Unexpired: immediately return decremented TTLs as `Cache Hit`.
- Expired with a running preload: join its result as `Prefetch Hit`; return fresh data or failure, never expired data. This path means a refresh result was shared and does not increment cache HitCount. CLI and logs preserve the path.
- Expired without a running preload, or absent: immediately resolve as `Upstream Query`, without consuming a background slot. Other clients and scans share that key's in-flight operation instead of issuing duplicate queries.

Successful cacheable refreshes replace only their original version and inherit its count. On a failed refresh, TTL 0 or an uncacheable winner (error RCODE or SOA-less negative answer), preserve an unexpired old answer with its original expiry, path and count; retry at that expiry rather than every 100 ms. Delete an already expired old entry, without serving stale data or retaining counts. Deleted names are not retried in the background; a later client may resolve again, with a new cached entry starting at HitCount 0.

Upstream TTL 0 answers, SOA-less NOERROR/NXDOMAIN responses, and error-RCODE responses are never cached or periodically preloaded. These winning responses are still delivered to waiters with their original RCODE. Negative lifetimes remain `min(SOA TTL, SOA.MINIMUM)`, further bounded by CNAME lifetimes. Background tasks can produce more network requests than the task limit through concurrent upstreams, CNAME follow-ups, or TCP retries; 100 ms is not a completion deadline.

Waiters receive independent copies of DNS results. Network I/O holds no cache or scheduling lock. Clear cancels existing tasks and wakes waiters; shutdown cancels and joins all resolution tasks and the scheduling loop.

#### Data structures

Hash lookup and three indexed min-heaps track LFU eviction, refresh deadlines and ready-task expiration priority. No expired-reclamation index or history pool remains. Priority updates, insertion/deletion and per-entry scheduling are O(log N); idle scans are O(1). Entries move to ready only when due, avoiding full-cache scans, sorting and DNS record cloning every 100 ms. Deletion removes all indices, preventing unbounded stale heap nodes. Cache space is O(N), plus the actual shared in-flight query table.

A rule can also add the IP addresses of the endpoints of its referenced upstreams to the corresponding nftables sets. Hostnames (including DoH URL hostnames) are resolved to IPs and refreshed periodically; new addresses are added to the sets, and old addresses are not automatically removed from shared sets.

### Basic UCI Example

```text
config updns 'general'
    option enabled '0'
    option port '153'
    option use_local_resolv '1'
    option local_resolv_file '/tmp/resolv.conf.d/resolv.conf.auto'
    option query_strategy '0'
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
| Settings & Port | `updns.general`: `enabled`, `port`, `use_local_resolv`, `local_resolv_file`, `query_strategy` | Enable/disable service, listen port, local resolver, resolution strategy |
| Upstream DNS | `updns.upstream`: `name`, `address`, `protocol`, `timeout` | Manage UDP/TCP/DoT/DoH upstreams |
| Domain Lists | `updns.domainlist`: `name`, `file` | Upload or manage list files referenced by rules |
| Cache | `updns.general`: `cache_enabled`, `cache_size`, `active_preload` | Control cache, capacity, and active preload |
| Logging | `updns.general`: `log_query`, `log_level` | Control query logging and log level |
| Rules | `updns.rule`: `name`, `domain_match`, `domain_lists`, `upstreams`, `action`, `ipv4_nftsets`, `ipv6_nftsets`, `add_upstream_ips` | Set match conditions, upstreams, response type, and nftables handling |

On a fresh install `enabled=0`, so the service does not start automatically. Defaults: port `153`, local resolver used, Dynamic Scheduling, cache enabled with capacity `4096`, active preload enabled, query logging off. The former `concurrent_query` field has no compatibility mapping: absence of `query_strategy` means `0` (Dynamic Scheduling). Former word-based values cause an error and must be replaced with their corresponding numbers before upgrading.

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

The page reads and saves configuration through UCI; local resolver entries appear as auto-generated `local-resolv-N` upstreams. Custom upstream entries can be dragged to change their order, while generated entries remain fixed at the top and cannot be edited, deleted, or reordered. The global upstream order is used for default upstreams when no rule matches; upstreams explicitly selected by a rule retain the order in that rule's list. Service status comes from rpcd `service.list`, the cache entry count from `/tmp/updns_cache_size`, and nftables sets from the page calling `nft -j list sets`. Domain lists are read and written through the rpcd file interface.

When changing the page, check the related UCI fields, the rpcd ACL, and backend behavior together. The ACL grants explicit file paths; when adding a new status file, file read/write path, or system command, update the ACL as well. Visible text uses `_()` and the PO translation must be kept in sync. If you change form sections or tab layout, review the mapping and ordering logic in the page.

Generated and custom upstreams use the same table columns and reorder, edit, and delete controls. All three controls are disabled for generated entries, with a tooltip explaining their automatic origin.
The upstream list retains column titles and hides only the column description row; the editor retains field titles and help text. Above the list, only local resolver import information or read errors are shown, without reorder instructions.
The protocol column uses inline SVG line icons and pill labels: amber for UDP, green for TCP, royal blue for DoH, and deep blue text on a pale blue background for DoT. Protocol text remains visible instead of relying on color alone. Generated and custom entries share this presentation; configuration values and the editor's protocol selector are unchanged.

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
│  Upstream     : A=Google-DoH / AAAA=Google-DoH
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

When a query fails the full report is still printed: `Response` shows `ERROR` and an `ERRORS` section gives a normalized short reason per query type (for example `Upstream timeout`, `Upstream connection refused`, `Upstream host unresolvable`, `Network not ready`, `No upstream configured`, `All upstreams failed`), and the command exits non-zero. If only one of A/AAAA fails, the other result is shown normally and the exit code is 0 as long as one succeeds.

`Upstream` shows each query type separately, for example `A=local-dhcp / AAAA=local-hosts`; missing sources appear as `(none)`. Cache hits show the original cached source, not a server contacted by this request.

The frame uses the actual terminal column count without minimum or maximum limits, falling back to `COLUMNS`, then 48 columns if unavailable. Text is aligned by Unicode display width and wrapped at grapheme boundaries. Long values continue at the value column; narrow terminals place labels and values on separate lines. Extremely narrow terminals reduce indentation and replace double-column graphemes that cannot fit with `?`. ANSI color/link escapes are removed from dynamic text and control characters become spaces. Some terminals may still render emoji and ambiguous-width characters differently.

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
- `stop` stops the service process; when it exits, its in-process cache no longer exists and its memory is reclaimed by the operating system. The init script then clears the nftables sets referenced by the current configuration.

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

A winning valid DNS response (including upstream error RCODEs such as `SERVFAIL` or `REFUSED`, and SOA-less negative responses) does not produce a query-failure warning; with `log_query` enabled it is logged as a regular per-query line with its actual RCODE. Only when no upstream yields a valid final response (timeouts, question mismatches, referrals, truncation, failed CNAME follow-ups, and so on) does the existing failure-logging policy emit a warning listing each upstream's specific cause.

Per-query lines identify the source as `QUERY [CLI]` or `QUERY [SRV]`, followed by the domain, query type, matched rule, query path, and DNS response code; an unmatched rule is shown as `-`. Server queries append `@client-address` when a client address is available and `#subnet` when EDNS Client Subnet (ECS) is present. Missing fields are omitted, so CLI queries have no client or ECS placeholders. dnsmasq can supply ECS when `add_subnet` is configured.

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
