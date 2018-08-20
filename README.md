# Domain list

List of domains, driven by Project V community. This list will be used by Project V, mainly for routing purpose.

## Structure of data

All data are under `data/` directory. Each file in the directory represents a sub-list of domains, named by the file name. File content is in the following format.

```
# comments
include:another-file
domain:google.com
keyword:google
regex:www\.google\.com
```

Syntax:

* Comments begins with `#`. It can start anywhere in the file. The content in the line after `#` is treated as comment and ignored in production.
* Inclusion begins with `include:`, followed by the file name of an existing file in the same directory.
* Subdomain begins with `domain:`, followed by a valid domain name. The prefix `domain:` may be omitted.
* Keyword begins with `keyword:`, followed by string.
* Regular expression begins with `regex:`, followed by a valid regular expression (per Golang's standard).

## How it works

The entire data directory will be built into an external `geosite` file for Project V. Each file in the directory represents a section in the generated file.

To generate a section:

1. Remove all the comments in the file.
1. Replace `include:` lines with the actual content of the file.
  * Inclusion replacement may be recursive.
  * Circular inclusion is allowed but has no effect.
1. Generate each `domain:` line into a [sub-domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L21).
1. Generate each `keyword:` line into a [plain domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L17).
1. Generate each `regex:` line into a [regex domain routing rule](https://github.com/v2ray/v2ray-core/blob/master/app/router/config.proto#L19)

## File name guideline

* A name represents a deterministic group of domains, by common understanding.
  * Good example: google, youtube, facebook
  * Bad example: blocked, evil, domestic
* A name may be divided into sub categories.
  * Example: ads-cn, ads-us

## Contribution guideline

* Please begin with small size PRs, say modification in a single file.
* A PR must be reviewed and approved by another member.
* After a few successful PRs, you may applied for manager access of this repository.
