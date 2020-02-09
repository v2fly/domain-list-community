# Domain list community

This project manages a list of domains, to be used as geosites for routing purpose in Project V.

## Purpose of this project

This project is not opinionated. In other words, it does NOT endorse, claim or imply that a domain should be blocked or proxied. It can be used to generate routing rules on demand.

## Structure of data

All data are under `data/` directory. Each file in the directory represents a sub-list of domains, named by the file name. File content is in the following format.

```
# comments
include:another-file
domain:google.com @attr1 @att2
keyword:google
regex:www\.google\.com
full:www.google.com
```

Syntax:

* Comment begins with `#`. It may begin anywhere in the file. The content in the line after `#` is treated as comment and ignored in production.
* Inclusion begins with `include:`, followed by the file name of an existing file in the same directory.
* Subdomain begins with `domain:`, followed by a valid domain name. The prefix `domain:` may be omitted.
* Keyword begins with `keyword:`, followed by a string.
* Regular expression begins with `regex:`, followed by a valid regular expression (per Golang's standard).
* Full domain begins with `full:`, followed by a complete and valid domain name.
* Domains (including `domain`, `keyword`, `regex` and `full`) may have one or more attributes. Each attribute begins with `@` and followed by the name of the attribute.

## How it works

The entire data directory will be built into an external `geosite` file for Project V. Each file in the directory represents a section in the generated file.

To generate a section:

1. Remove all the comments in the file.
2. Replace `include:` lines with the actual content of the file.
3. Omit all empty lines.
4. Generate each `domain:` line into a [sub-domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L21).
5. Generate each `keyword:` line into a [plain domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L17).
6. Generate each `regex:` line into a [regex domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L19).
7. Generate each `full:` line into a [full domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L23).

## How to organize domains

### File name

Theoretically any string can be used as the name, as long as it is a valid file name. In practice, we prefer names for determinic group of domains, such as the owner (usually a company name) of the domains, e.g., "google", "netflix". Names with unclear scope are generally unrecommended, such as "evil", or "local".

### Attributes

Attribute is useful for sub-group of domains, especially for filtering purpose. For example, the list of "google" domains may contains its main domains, as well as domains that serve ads. The ads domains may be marked by attribute "@ads", and can be used as "geosite:google@ads" in V2Ray routing.

## Contribution guideline

* Please begin with small size PRs, say modification in a single file.
* A PR must be reviewed and approved by another member.
* After a few successful PRs, you may apply for manager access of this repository.
