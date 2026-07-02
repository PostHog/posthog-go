//go:build !linux && !darwin

package posthog

// loadMainImage is unsupported on this platform: frames still carry raw
// addresses, but no debug image is reported, so they fall back to the
// runtime-resolved function, file, and line.
func loadMainImage() mainImageInfo {
	return mainImageInfo{}
}
