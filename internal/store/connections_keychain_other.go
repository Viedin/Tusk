//go:build !darwin || !cgo

package store

import "errors"

func loadKeychainConnectionKey(service, account string, create bool) ([]byte, error) {
	return nil, errors.New("Saved connection encryption requires macOS Keychain with a native build")
}
