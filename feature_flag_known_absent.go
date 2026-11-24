package posthog

import (
	"fmt"
	"strings"
)

// maxKnownAbsentPropertyKeys is the most keys the /flags endpoint accepts in
// PersonPropertyKeysKnownAbsent, and in each group type's list in
// GroupPropertyKeysKnownAbsent. Duplicates count toward the limit.
const maxKnownAbsentPropertyKeys = 1000

// personMetadataKeyPrefix marks persons-table fields such as created_at, which
// known absence never hides.
const personMetadataKeyPrefix = "__posthog_person_metadata__"

type propertyKeySet map[string]struct{}

// knownAbsentPropertyKeys holds the caller's known-absent property keys, both as
// sent to /flags and as lookup sets for local evaluation. A nil value lists nothing.
type knownAbsentPropertyKeys struct {
	personKeys []string
	groupKeys  map[string][]string
	person     propertyKeySet
	groups     map[string]propertyKeySet
}

// newKnownAbsentPropertyKeys returns nil when the caller listed no keys.
func newKnownAbsentPropertyKeys(personKeys []string, groupKeys map[string][]string) *knownAbsentPropertyKeys {
	if len(personKeys) == 0 && len(groupKeys) == 0 {
		return nil
	}
	knownAbsent := &knownAbsentPropertyKeys{}
	if len(personKeys) > 0 {
		knownAbsent.personKeys = personKeys
		for _, key := range personKeys {
			if strings.HasPrefix(key, personMetadataKeyPrefix) {
				continue
			}
			if knownAbsent.person == nil {
				knownAbsent.person = make(propertyKeySet, len(personKeys))
			}
			knownAbsent.person[key] = struct{}{}
		}
	}
	for groupType, keys := range groupKeys {
		// /flags rejects a null group list, so empty lists are left out of the body too.
		if len(keys) == 0 {
			continue
		}
		if knownAbsent.groups == nil {
			knownAbsent.groupKeys = make(map[string][]string, len(groupKeys))
			knownAbsent.groups = make(map[string]propertyKeySet, len(groupKeys))
		}
		knownAbsent.groupKeys[groupType] = keys
		set := make(propertyKeySet, len(keys))
		for _, key := range keys {
			set[key] = struct{}{}
		}
		knownAbsent.groups[groupType] = set
	}
	return knownAbsent
}

func (k *knownAbsentPropertyKeys) personSet() propertyKeySet {
	if k == nil {
		return nil
	}
	return k.person
}

func (k *knownAbsentPropertyKeys) groupSet(groupType string) propertyKeySet {
	if k == nil {
		return nil
	}
	return k.groups[groupType]
}

func (k *knownAbsentPropertyKeys) wirePersonKeys() []string {
	if k == nil {
		return nil
	}
	return k.personKeys
}

func (k *knownAbsentPropertyKeys) wireGroupKeys() map[string][]string {
	if k == nil {
		return nil
	}
	return k.groupKeys
}

// validateKnownAbsentPropertyKeys rejects lists over the /flags limit instead of
// truncating them.
func validateKnownAbsentPropertyKeys(personKeys []string, groupKeys map[string][]string) error {
	if len(personKeys) > maxKnownAbsentPropertyKeys {
		return ConfigError{
			Reason: fmt.Sprintf("PersonPropertyKeysKnownAbsent allows at most %d keys", maxKnownAbsentPropertyKeys),
			Field:  "PersonPropertyKeysKnownAbsent",
			Value:  len(personKeys),
		}
	}
	for groupType, keys := range groupKeys {
		if len(keys) > maxKnownAbsentPropertyKeys {
			return ConfigError{
				Reason: fmt.Sprintf("GroupPropertyKeysKnownAbsent allows at most %d keys per group type (group type %q)", maxKnownAbsentPropertyKeys, groupType),
				Field:  "GroupPropertyKeysKnownAbsent",
				Value:  len(keys),
			}
		}
	}
	return nil
}

// knownAbsentPropertyMatch is the definitive result of a supported operator with a
// condition value on a known-absent key: the negated operators match and every other operator does not.
func knownAbsentPropertyMatch(operator string) bool {
	switch operator {
	case "is_not_set", "is_not", "not_icontains", "not_starts_with", "not_ends_with", "not_regex":
		return true
	}
	return false
}
