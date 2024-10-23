# Domain list community

This project manages a list of domain.


## ある変更

```geosite:cdn ```
>  CDN domain list from [ruleset.skk.moe](https://ruleset.skk.moe/Clash/domainset/cdn.txt).

```geosite:anti-ad```
>  List ```anti-ad-domains.txt``` from [privacy-protection-tools/anti-AD](https://github.com/privacy-protection-tools/anti-AD).

```geosite:skk-reject```
> List ```reject.txt``` from [ruleset.skk.moe](https://ruleset.skk.moe/Clash/domainset/reject.txt).

```geosite:dns-unlock```
> List of common domains for streaming unblocking services. from a certain unlocking service provider.
> ### Check whether you have a valid corresponding unlocking service before use

```geosite:normal-proxy```
> List from personal clash rule.

```geosite:nojp-proxy```
> List from personal clash rule. Recommended outside Japan.

```geosite:emby-s ```
>  Domain list of some emby servers.

```geosite:geolocation-!cn```

> \+ offcloud.com

~~```geosite:cn```~~
>  ~~Rules from [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat/raw/refs/heads/meta/geo/geosite/cn.list)~~

>  ~~Can find original list in ```geosite:cn-bak```~~

## 個人用リスト
```geosite:reject-list```
> include : ```geosite:anti-ad``` ```geosite:category-ads-all``` 

```geosite:skk-reject```  addblocker

```geosite:dns-unlock```  streaming unblock

```geosite:normal-proxy``` PROXY

```geosite:nojp-proxy``` addproxy

```geosite:emby-s ``` emby servers

```geosite:user-Mobilegame ``` Mobile game(Almost JP)

```geosite:user-PCgame ``` PC game

## Generate `geosite.dat` manually

- Install `golang` and `git`
- Clone project code
- Navigate to project root directory: `cd domain-list-community`
- Install project dependencies: `go mod download`
- Generate `geosite.dat` (without `datapath` option means to use domain lists in `data` directory of current working directory):
  - `go run ./`
  - `go run ./ --datapath=/path/to/your/custom/data/directory`

Run `go run ./ --help` for more usage information.

## Structure of data

All data are under `data` directory. Each file in the directory represents a sub-list of domains, named by the file name. File content is in the following format.

```
# comments
include:another-file
domain:google.com @attr1 @attr2
keyword:google
regexp:www\.google\.com$
full:www.google.com
```

**Syntax:**

> The following types of rules are **NOT** fully compatible with the ones that defined by user in V2Ray config file. Do **Not** copy and paste directly.

* Comment begins with `#`. It may begin anywhere in the file. The content in the line after `#` is treated as comment and ignored in production.
* Inclusion begins with `include:`, followed by the file name of an existing file in the same directory.
* Subdomain begins with `domain:`, followed by a valid domain name. The prefix `domain:` may be omitted.
* Keyword begins with `keyword:`, followed by a string.
* Regular expression begins with `regexp:`, followed by a valid regular expression (per Golang's standard).
* Full domain begins with `full:`, followed by a complete and valid domain name.
* Domains (including `domain`, `keyword`, `regexp` and `full`) may have one or more attributes. Each attribute begins with `@` and followed by the name of the attribute.

## How it works

The entire `data` directory will be built into an external `geosite` file for Project V. Each file in the directory represents a section in the generated file.

To generate a section:

1. Remove all the comments in the file.
2. Replace `include:` lines with the actual content of the file.
3. Omit all empty lines.
4. Generate each `domain:` line into a [sub-domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/config.proto#L21).
5. Generate each `keyword:` line into a [plain domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/config.proto#L17).
6. Generate each `regexp:` line into a [regex domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/config.proto#L19).
7. Generate each `full:` line into a [full domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/config.proto#L23).

## How to organize domains

### File name

Theoretically any string can be used as the name, as long as it is a valid file name. In practice, we prefer names for determinic group of domains, such as the owner (usually a company name) of the domains, e.g., "google", "netflix". Names with unclear scope are generally unrecommended, such as "evil", or "local".

### Attributes

Attribute is useful for sub-group of domains, especially for filtering purpose. For example, the list of `google` domains may contains its main domains, as well as domains that serve ads. The ads domains may be marked by attribute `@ads`, and can be used as `geosite:google@ads` in V2Ray routing.

