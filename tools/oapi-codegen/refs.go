package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	oasyaml "github.com/oasdiff/yaml"
	"gopkg.in/yaml.v3"
)

// Keys that make a loader read another document whatever their value.
var loadingKeys = []string{"$id", "$dynamicRef", "externalValue"}

// is compares a key the way encoding/json matches field names (kin-openapi
// decodes through it), so "$REF" counts as "$ref".
func is(key, name string) bool { return strings.EqualFold(key, name) }

// nonLocalInContract returns why the contract could make the loader read
// another document, or "". It decodes the contract the way kin-openapi does
// (JSON first, then oasdiff/yaml), so escaped keys, anchors and aliases are
// seen as the loader sees them.
func nonLocalInContract(data []byte) (string, error) {
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		if _, err := oasyaml.Unmarshal(data, &tree, oasyaml.DecodeOpts{DisableTimestamps: true}); err != nil {
			return "", fmt.Errorf("parse: %w", err)
		}
	}
	return nonLocal(tree), nil
}

// nonLocalInOverlay is nonLocalInContract for the overlay, decoded the way
// the overlay library does (yaml.v3). Its update values end up in the
// contract, and its extends field names another document.
func nonLocalInOverlay(data []byte) (string, error) {
	var tree any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	var keys []string
	switch root := tree.(type) {
	case map[string]any:
		for key := range root {
			keys = append(keys, key)
		}
	case map[any]any:
		for key := range root {
			keys = append(keys, fmt.Sprint(key))
		}
	}
	for _, key := range keys {
		if is(key, "extends") {
			return "#/" + key + " is not allowed", nil
		}
	}
	return nonLocal(tree), nil
}

// nonLocal walks a decoded document. Allowed: $ref and discriminator mapping
// values that point into this document ("#/..."); a discriminator itself
// must be written inline, since a $ref there would be resolved as a mapping.
func nonLocal(tree any) string {
	var found string
	var visit func(node any, at string, parentIsDiscriminator, inDiscriminator bool)
	entry := func(key string, value any, at string, parentIsDiscriminator, inDiscriminator bool) {
		where := at + "/" + key
		switch {
		case slices.ContainsFunc(loadingKeys, func(k string) bool { return is(key, k) }):
			found = where + " is not allowed"
		case is(key, "$ref") && inDiscriminator:
			found = where + ": a discriminator must not use $ref"
		case is(key, "$ref") && !isLocal(value):
			found = where + " is not a local reference"
		case is(key, "mapping") && parentIsDiscriminator:
			if m, ok := value.(map[string]any); ok {
				for name, target := range m {
					if !isLocal(target) {
						found = where + "/" + name + " is not a local reference"
						return
					}
				}
			} else if value != nil {
				found = where + " is not a map of local references"
			}
		}
		if found == "" {
			visit(value, where, is(key, "discriminator"), inDiscriminator || is(key, "discriminator"))
		}
	}
	visit = func(node any, at string, parentIsDiscriminator, inDiscriminator bool) {
		switch n := node.(type) {
		case []any:
			for i, item := range n {
				if found == "" {
					visit(item, at+"/"+strconv.Itoa(i), false, inDiscriminator)
				}
			}
		case map[string]any:
			for key, value := range n {
				if found == "" {
					entry(key, value, at, parentIsDiscriminator, inDiscriminator)
				}
			}
		case map[any]any:
			for key, value := range n {
				if found == "" {
					entry(fmt.Sprint(key), value, at, parentIsDiscriminator, inDiscriminator)
				}
			}
		}
	}
	visit(tree, "#", false, false)
	return found
}

func isLocal(value any) bool {
	s, ok := value.(string)
	return ok && strings.HasPrefix(s, "#/")
}
