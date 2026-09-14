//go:build !linux

package cap

// cInit performs the lazy identification of the capability vintage of
// the running system.
func cInit() {
	if maxValues == 0 {
		// Fall back to using the largest value defined at build time.
		maxValues = NamedCount
	}
}
