package posthog

type SizeLimitedMap struct {
	ids  map[string][]string
	size int
}

const SIZE_DEFAULT = 50_000

func newSizeLimitedMap() *SizeLimitedMap {
	newMap := SizeLimitedMap{
		ids:  map[string][]string{},
		size: SIZE_DEFAULT,
	}

	return &newMap
}

func (sizeLimitedMap *SizeLimitedMap) add(key string, element string) {

	if len(sizeLimitedMap.ids) >= sizeLimitedMap.size {
		sizeLimitedMap.ids = nil
	}

	if val, ok := sizeLimitedMap.ids[key]; ok {
		sizeLimitedMap.ids[key] = append(val, element)
	} else {
		sizeLimitedMap.ids[key] = []string{element}
	}
}

func (sizeLimitedMap *SizeLimitedMap) contains(key string, element string) bool {
	if val, ok := sizeLimitedMap.ids[key]; ok {
		for _, v := range val {
			if v == element {
				return true
			}
		}
	}

	return false
}
