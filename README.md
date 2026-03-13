# Domain list community

This project manages a list of domains, to be used as geosites for routing purpose in Project V.

## Purpose of this project

This project is not opinionated. In other words, it does NOT endorse, claim or imply that a domain should be blocked or proxied. It can be used to generate routing rules on demand.

## Download links

- **dlc.dat**：[https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat](https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat)
- **dlc.dat.sha256sum**：[https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat.sha256sum](https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat.sha256sum)
- **dlc.dat_plain.yml**：[https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat_plain.yml](https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat_plain.yml)
- **dlc.dat_plain.yml.sha256sum**：[https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat_plain.yml.sha256sum](https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat_plain.yml.sha256sum)

## Notice

- Rules with `@!cn` attribute has been cast out from cn lists. `geosite:geolocation-cn@!cn` is no longer available. Check [#390](https://github.com/v2fly/domain-list-community/issues/390), [#3119](https://github.com/v2fly/domain-list-community/pull/3119) and [#3198](https://github.com/v2fly/domain-list-community/pull/3198) for more information.
- Dedicated non-category ad lists like `geosite:xxx-ads` has been removed. Use `geosite:xxx@ads` instead. `geosite:category-ads[-xx]` is not affected.

Please report if you have any problems or questions.

## Usage example

Each file in the `data` directory can be used as a rule in this format: `geosite:filename`.

```json
"routing": {
  "domainStrategy": "IPIfNonMatch",
  "rules": [
    {
      "type": "field",
      "outboundTag": "Reject",
      "domain": [
        "geosite:category-ads-all",
        "geosite:category-porn"
      ]
    },
    {
      "type": "field",
      "outboundTag": "Direct",
      "domain": [
        "domain:icloud.com",
        "domain:icloud-content.com",
        "domain:cdn-apple.com",
        "geosite:cn",
        "geosite:private"
      ]
    },
    {
      "type": "field",
      "outboundTag": "Proxy-1",
      "domain": [
        "geosite:category-anticensorship",
        "geosite:category-media",
        "geosite:category-vpnservices"
      ]
    },
    {
      "type": "field",
      "outboundTag": "Proxy-2",
      "domain": [
        "geosite:category-dev"
      ]
    },
    {
      "type": "field",
      "outboundTag": "Proxy-3",
      "domain": [
        "geosite:geolocation-!cn"
      ]
    }
  ]
}
```

## Generate `dlc.dat` manually

- Install `golang` and `git`
- Clone project code: `git clone https://github.com/v2fly/domain-list-community.git`
- Navigate to project root directory: `cd domain-list-community`
- Install project dependencies: `go mod download`
- Generate `dlc.dat` (without `datapath` option means to use domain lists in `data` directory of current working directory):
  - `go run ./`
  - `go run ./ --datapath=/path/to/your/custom/data/directory`

Run `go run ./ --help` for more usage information.

## Structure of data

All data are under `data` directory. Each file in the directory represents a sub-list of domains, named by the file name. File content is in the following format.

```
# comments
include:another-file
domain:google.com @attr1 @attr2
full:analytics.google.com @ads
keyword:google
regexp:^odd[1-7]\.example\.org(\.[a-z]{2})?$
```

**Syntax:**

> [!NOTE]
> Adding new `regexp` and `keyword` rules is discouraged because it is easy to use them incorrectly, and proxy software cannot efficiently match these types of rules.

> [!NOTE]
> The following types of rules are **NOT** fully compatible with the ones that defined by user in V2Ray config file. Do **Not** copy and paste directly.

- Comment begins with `#`. It may begin anywhere in the file. The content in the line after `#` is treated as comment and ignored in production.
- Subdomain begins with `domain:`, followed by a valid domain name. The prefix `domain:` may be omitted.
- Full domain begins with `full:`, followed by a complete and valid domain name.
- Keyword begins with `keyword:`, followed by a substring of a valid domain name.
- Regular expression begins with `regexp:`, followed by a valid regular expression (per Golang's standard).
- Domain rules (including `domain`, `full`, `keyword`, and `regexp`) may have none, one or more attributes. Each attribute begins with `@` and followed by the name of the attribute. Attributes will remain available in final lists and `dlc.dat`.
- Domain rules may have none, one or more affiliations, which additionally adds the domain rule into the affiliated target list. Each affiliation begins with `&` and followed by the name of the target list (nomatter whether the target has a dedicated file in data path). This is a method for data management, and will not remain in the final lists or `dlc.dat`.
- Inclusion begins with `include:`, followed by the name of another valid domain list. A simple `include:listb` in file `lista` means adding all domain rules of `listb` into `lista`. Inclusions with attributes stands for selective inclusion. `include:listb @attr1 @-attr2` means only adding those domain rules *with* `@attr1` **and** *without* `@attr2`. This is a special type for data management, and will not remain in the final lists or `dlc.dat`.

## How it works

The entire `data` directory will be built into an external `geosite` file for Project V. Each file in the directory represents a section in the generated file.

**General steps:**

1. Read files in the data path (ignore all comments and empty lines).
2. Parse and resolve source data, turn affiliations and inclusions into actual domain rules in proper lists.
3. Deduplicate and sort rules in every list.
4. Export desired plain text lists.
5. Generate `dlc.dat`:
   - turn each `domain:` line into a [sub-domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/routercommon/common.proto#L21).
   - turn each `full:` line into a [full domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/routercommon/common.proto#L23).
   - turn each `keyword:` line into a [plain domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/routercommon/common.proto#L17).
   - turn each `regexp:` line into a [regex domain routing rule](https://github.com/v2fly/v2ray-core/blob/master/app/router/routercommon/common.proto#L19).

Read [main.go](./main.go) for details.

## How to organize domains

### File name

Theoretically any string can be used as the name, as long as it is a valid file name. In practice, we prefer names for determinic group of domains, such as the owner (usually a company name) of the domains, e.g., "google", "netflix". Names with unclear scope are generally unrecommended, such as "evil", or "local".

### Attributes

Attribute is useful for sub-group of domains, especially for filtering purpose. For example, the list of `google` may contains its main domains, as well as domains that serve ads. The ads domains may be marked by attribute `@ads`, and can be used as `geosite:google@ads` in V2Ray routing. Domains and services that originate from outside China mainland but have access point in China mainland, may be marked by attribute `@cn`.

## Contribution guideline

- Fork this repo, make modifications to your own repo, file a PR.
- Please begin with small size PRs, say modification in a single file.
- A PR must be reviewed and approved by another member.
- A script will verify your pull request to test whether your PR is correct or not every time you update the PR. Only the PR which passes the test will be merged. Please go to the Action label to get detailed information if you didn't pass it. We also provide the file which has been generated to make you test.
- After a few successful PRs, you may apply for manager access to this repository.
