// Package browser provides a cross-platform function for opening a URL
// in the system's default web browser.
package browser

// Open opens the given URL in the default system browser.
// It returns an error if the browser cannot be launched.
func Open(url string) error {
	return openBrowser(url)
}
