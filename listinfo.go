package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"v2ray.com/core/app/router"
)

// ListInfo is the information structure of a single file in data directory.
// It includes all types of rules of the file, as well as servel types of
// sturctures of same items for convenience in later process.
type ListInfo struct {
	Name                    fileName
	HasInclusion            bool
	InclusionAttributeMap   map[fileName][]attribute
	FullTypeList            []*router.Domain
	KeywordTypeList         []*router.Domain
	RegexpTypeList          []*router.Domain
	AttributeRuleUniqueList []*router.Domain
	DomainTypeList          []*router.Domain
	DomainTypeUniqueList    []*router.Domain
	AttributeRuleListMap    map[attribute][]*router.Domain
	GeoSite                 *router.GeoSite
}

// NewListInfo return a ListInfo
func NewListInfo() *ListInfo {
	return &ListInfo{
		HasInclusion:            false,
		InclusionAttributeMap:   make(map[fileName][]attribute),
		FullTypeList:            make([]*router.Domain, 0, 10),
		KeywordTypeList:         make([]*router.Domain, 0, 10),
		RegexpTypeList:          make([]*router.Domain, 0, 10),
		AttributeRuleUniqueList: make([]*router.Domain, 0, 10),
		DomainTypeList:          make([]*router.Domain, 0, 10),
		DomainTypeUniqueList:    make([]*router.Domain, 0, 10),
		AttributeRuleListMap:    make(map[attribute][]*router.Domain),
	}
}

// ProcessList processes each line of every single file in the data directory
// and generates a ListInfo of each file.
func (l *ListInfo) ProcessList(file *os.File) error {
	scanner := bufio.NewScanner(file)
	// Parse a file line by line to generate ListInfo
	for scanner.Scan() {
		line := scanner.Text()
		if isEmpty(line) {
			continue
		}
		line = removeComment(line)
		if isEmpty(line) {
			continue
		}
		parsedRule, err := l.parseRule(line)
		if err != nil {
			return err
		}
		l.classifyRule(parsedRule)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// parseRule parses a single rule
func (l *ListInfo) parseRule(line string) (*router.Domain, error) {
	line = strings.TrimSpace(line)
	parts := strings.Split(line, " ")

	if len(parts) == 0 {
		return nil, errors.New("empty rule")
	}

	ruleWithType := strings.TrimSpace(parts[0])
	if len(ruleWithType) == 0 {
		return nil, errors.New("empty rule")
	}

	var rule router.Domain
	if err := l.parseDomain(ruleWithType, &rule); err != nil {
		return nil, err
	}

	for i := 1; i < len(parts); i++ {
		partI := strings.TrimSpace(parts[i])
		if len(partI) == 0 {
			continue
		}
		attr, err := l.parseAttribute(partI)
		if err != nil {
			return nil, err
		}
		rule.Attribute = append(rule.Attribute, attr)
	}

	return &rule, nil
}

func (l *ListInfo) parseDomain(domain string, rule *router.Domain) error {
	kv := strings.Split(domain, ":")
	switch len(kv) {
	case 1: // line without type prefix
		rule.Type = router.Domain_Domain
		rule.Value = strings.ToLower(kv[0])
	case 2: // line with type/include prefix
		ruleType := strings.TrimSpace(kv[0])
		ruleVal := strings.TrimSpace(kv[1])
		switch ruleType {
		case "include": // line begins with "include"
			l.HasInclusion = true
			kv2 := strings.Split(ruleVal, "@")
			filename := fileName(strings.ToUpper(strings.TrimSpace(kv2[0])))
			switch len(kv2) {
			case 1: // Inclusion without attribute
				// Use '@' as the placeholder attribute for 'include:filename'
				l.InclusionAttributeMap[filename] = append(l.InclusionAttributeMap[filename], attribute("@"))
			case 2: // Inclusion with attribute
				// Added in this format: '@cn'
				l.InclusionAttributeMap[filename] = append(l.InclusionAttributeMap[filename], attribute("@"+strings.TrimSpace(kv2[1])))
			default:
				return errors.New("invalid format for inclusion: " + domain)
			}
		default: // line begins with "full" / "domain" / "regexp" / "keyword"
			rule.Value = strings.ToLower(ruleVal)
			switch ruleType {
			case "full":
				rule.Type = router.Domain_Full
			case "domain":
				rule.Type = router.Domain_Domain
			case "keyword":
				rule.Type = router.Domain_Plain
			case "regexp":
				rule.Type = router.Domain_Regex
			default:
				return errors.New("unknown domain type: " + ruleType)
			}
		}
	default:
		return errors.New("invalid format: " + domain)
	}
	return nil
}

func (l *ListInfo) parseAttribute(attr string) (*router.Domain_Attribute, error) {
	if attr[0] != '@' {
		return nil, errors.New("invalid attribute: " + attr)
	}
	attr = attr[1:] // Trim out attribute prefix `@` character

	var attribute router.Domain_Attribute
	attribute.Key = strings.ToLower(attr)
	attribute.TypedValue = &router.Domain_Attribute_BoolValue{BoolValue: true}
	return &attribute, nil
}

// classifyRule classifies a single rule and write into *ListInfo
func (l *ListInfo) classifyRule(rule *router.Domain) {
	if len(rule.Attribute) > 0 {
		l.AttributeRuleUniqueList = append(l.AttributeRuleUniqueList, rule)
		var attrsString attribute
		for _, attr := range rule.Attribute {
			attrsString += attribute("@" + attr.GetKey()) // attrsString will be "@cn@ads" if there are more than one attribute
		}
		l.AttributeRuleListMap[attrsString] = append(l.AttributeRuleListMap[attrsString], rule)
	} else {
		switch rule.Type {
		case router.Domain_Full:
			l.FullTypeList = append(l.FullTypeList, rule)
		case router.Domain_Domain:
			l.DomainTypeList = append(l.DomainTypeList, rule)
		case router.Domain_Plain:
			l.KeywordTypeList = append(l.KeywordTypeList, rule)
		case router.Domain_Regex:
			l.RegexpTypeList = append(l.RegexpTypeList, rule)
		}
	}
}

// Flatten flattens the rules in a file that have "include" syntax
// in data directory, and adds those need-to-included rules into it.
// This feature supports the "include:filename@attribute" syntax.
// It also generates a domain trie of domain-typed rules for each file
// to remove duplications of them.
func (l *ListInfo) Flatten(lm *ListInfoMap) error {
	if l.HasInclusion {
		for filename, attrs := range l.InclusionAttributeMap {
			for _, attrWanted := range attrs {
				includedList := (*lm)[filename]
				switch string(attrWanted) {
				case "@":
					l.FullTypeList = append(l.FullTypeList, includedList.FullTypeList...)
					l.DomainTypeList = append(l.DomainTypeList, includedList.DomainTypeList...)
					l.KeywordTypeList = append(l.KeywordTypeList, includedList.KeywordTypeList...)
					l.RegexpTypeList = append(l.RegexpTypeList, includedList.RegexpTypeList...)
					l.AttributeRuleUniqueList = append(l.AttributeRuleUniqueList, includedList.AttributeRuleUniqueList...)
					for attr, domainList := range includedList.AttributeRuleListMap {
						l.AttributeRuleListMap[attr] = append(l.AttributeRuleListMap[attr], domainList...)
					}

				default:
					for attr, domainList := range includedList.AttributeRuleListMap {
						// If there are more than one attribute attached to the rule,
						// the attribute key of AttributeRuleListMap in ListInfo
						// will be like: "@cn@ads".
						// So if to extract rules with a specific attribute, it is necessary
						// also to test the multi-attribute keys of AttributeRuleListMap.
						// Notice: if "include:google@cn" and "include:google@ads" appear
						// at the same time in the parent list. There are chances that the same
						// rule with two attributes will be included twice in the parent list.
						if strings.Contains(string(attr)+"@", string(attrWanted)+"@") {
							l.AttributeRuleListMap[attr] = append(l.AttributeRuleListMap[attr], domainList...)
							l.AttributeRuleUniqueList = append(l.AttributeRuleUniqueList, domainList...)
						}
					}
				}
			}
		}
	}

	sort.Slice(l.DomainTypeList, func(i, j int) bool {
		return len(strings.Split(l.DomainTypeList[i].GetValue(), ".")) < len(strings.Split(l.DomainTypeList[j].GetValue(), "."))
	})

	trie := NewDomainTrie()
	for _, domain := range l.DomainTypeList {
		success, err := trie.Insert(domain.GetValue())
		if err != nil {
			return err
		}
		if success {
			l.DomainTypeUniqueList = append(l.DomainTypeUniqueList, domain)
		}
	}

	return nil
}

// ToGeoSite converts every ListInfo into a router.GeoSite structure.
// It also excludes rules with certain attributes in certain files that
// user specified in command line when runing the program.
func (l *ListInfo) ToGeoSite(excludeAttrs map[fileName]map[attribute]bool) {
	geosite := new(router.GeoSite)
	geosite.CountryCode = string(l.Name)
	geosite.Domain = append(geosite.Domain, l.FullTypeList...)
	geosite.Domain = append(geosite.Domain, l.DomainTypeUniqueList...)
	geosite.Domain = append(geosite.Domain, l.RegexpTypeList...)

	for _, keywordRule := range l.KeywordTypeList {
		if len(strings.TrimSpace(keywordRule.GetValue())) > 0 {
			geosite.Domain = append(geosite.Domain, keywordRule)
		}
	}

	if excludeAttrs != nil && excludeAttrs[l.Name] != nil {
		excludeAttrsMap := excludeAttrs[l.Name]
		for _, domain := range l.AttributeRuleUniqueList {
			ifKeep := true
			for _, attr := range domain.GetAttribute() {
				if excludeAttrsMap[attribute(attr.GetKey())] {
					ifKeep = false
					break
				}
			}
			if ifKeep {
				geosite.Domain = append(geosite.Domain, domain)
			}
		}
	} else {
		geosite.Domain = append(geosite.Domain, l.AttributeRuleUniqueList...)
	}
	l.GeoSite = geosite
}

// ToPlainText convert router.GeoSite structure to plaintext format.
func (l *ListInfo) ToPlainText() []byte {
	plaintextBytes := make([]byte, 0, 1024*512)

	for _, rule := range l.GeoSite.Domain {
		ruleVal := strings.TrimSpace(rule.GetValue())
		if len(ruleVal) == 0 {
			continue
		}

		var ruleString string
		switch rule.Type {
		case router.Domain_Full:
			ruleString = "full:" + ruleVal
		case router.Domain_Domain:
			ruleString = "domain:" + ruleVal
		case router.Domain_Plain:
			ruleString = "keyword:" + ruleVal
		case router.Domain_Regex:
			ruleString = "regexp:" + ruleVal
		}

		if len(rule.Attribute) > 0 {
			ruleString += ":"
			for _, attr := range rule.Attribute {
				ruleString += "@" + attr.GetKey() + ","
			}
			ruleString = strings.TrimRight(ruleString, ",")
		}
		// Output format is: type:domain.tld:@attr1,@attr2
		plaintextBytes = append(plaintextBytes, []byte(ruleString+"\n")...)
	}

	return plaintextBytes
}

// ToGFWList converts router.GeoSite to GFWList format.
func (l *ListInfo) ToGFWList() []byte {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	timeString := fmt.Sprintf("! Last Modified: %s\n", time.Now().In(loc).Format(time.RFC1123))

	gfwlistBytes := make([]byte, 0, 1024*512)
	gfwlistBytes = append(gfwlistBytes, []byte("[AutoProxy 0.2.9]\n")...)
	gfwlistBytes = append(gfwlistBytes, []byte(timeString)...)
	gfwlistBytes = append(gfwlistBytes, []byte("! Expires: 24h\n")...)
	gfwlistBytes = append(gfwlistBytes, []byte("! HomePage: https://github.com/v2fly/domain-list-community\n")...)
	gfwlistBytes = append(gfwlistBytes, []byte("! GitHub URL: https://raw.githubusercontent.com/v2fly/domain-list-community/release/gfwlist.txt\n")...)
	gfwlistBytes = append(gfwlistBytes, []byte("! jsdelivr URL: https://cdn.jsdelivr.net/gh/v2fly/domain-list-community@release/gfwlist.txt\n")...)
	gfwlistBytes = append(gfwlistBytes, []byte("\n")...)

	for _, rule := range l.GeoSite.Domain {
		ruleVal := strings.TrimSpace(rule.GetValue())
		if len(ruleVal) == 0 {
			continue
		}

		switch rule.Type {
		case router.Domain_Full:
			gfwlistBytes = append(gfwlistBytes, []byte("|http://"+ruleVal+"\n")...)
			gfwlistBytes = append(gfwlistBytes, []byte("|https://"+ruleVal+"\n")...)
		case router.Domain_Domain:
			gfwlistBytes = append(gfwlistBytes, []byte("||"+ruleVal+"\n")...)
		case router.Domain_Plain:
			gfwlistBytes = append(gfwlistBytes, []byte(ruleVal+"\n")...)
		case router.Domain_Regex:
			gfwlistBytes = append(gfwlistBytes, []byte("/"+ruleVal+"/\n")...)
		}
	}

	return gfwlistBytes
}
