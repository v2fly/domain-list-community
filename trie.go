package main

import (
	"errors"
	"strings"
)

type node struct {
	leaf     bool
	children map[string]*node
}

func newNode() *node {
	return &node{
		leaf:     false,
		children: make(map[string]*node),
	}
}

func (n *node) getChild(s string) *node {
	return n.children[s]
}

func (n *node) hasChild(s string) bool {
	return n.getChild(s) != nil
}

func (n *node) addChild(s string, child *node) {
	n.children[s] = child
}

func (n *node) setLeaf() {
	n.leaf = true
}

func (n *node) isLeaf() bool {
	return n.leaf
}

// DomainTrie is a domain trie for domain type rules.
type DomainTrie struct {
	root *node
}

// NewDomainTrie creates and returns a new domain trie.
func NewDomainTrie() *DomainTrie {
	return &DomainTrie{
		root: newNode(),
	}
}

// Insert inserts a domain rule string into the domain trie
// and return whether is inserted successfully or not.
func (t *DomainTrie) Insert(domain string) (bool, error) {
	if domain == "" {
		return false, errors.New("empty domain")
	}
	parts := strings.Split(domain, ".")

	node := t.root
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]

		if node.isLeaf() {
			return false, nil
		}
		if !node.hasChild(part) {
			node.addChild(part, newNode())
			if i == 0 {
				node.getChild(part).leaf = true
				return true, nil
			}
		}
		node = node.getChild(part)
	}
	return false, nil
}
