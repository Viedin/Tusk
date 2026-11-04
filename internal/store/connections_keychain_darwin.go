//go:build darwin && cgo

package store

import (
	"crypto/rand"
	"errors"

	"github.com/keybase/go-keychain"
)

func loadKeychainConnectionKey(service, account string, create bool) ([]byte, error) {
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(service)
	query.SetAccount(account)
	query.SetSynchronizable(keychain.SynchronizableNo)
	query.SetMatchLimit(keychain.MatchLimitOne)
	query.SetReturnData(true)
	results, err := keychain.QueryItem(query)
	if err != nil {
		return nil, errors.New("macOS Keychain is unavailable or access was denied")
	}
	if len(results) == 1 {
		if len(results[0].Data) != 32 {
			return nil, errors.New("macOS Keychain contains an invalid saved connection key")
		}
		return results[0].Data, nil
	}
	if !create {
		return nil, errors.New("The saved connection key is missing from macOS Keychain")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("Could not generate a saved connection encryption key")
	}
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount(account)
	item.SetLabel("Tusk saved connection encryption")
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetData(key)
	if err := keychain.AddItem(item); err != nil {
		if errors.Is(err, keychain.ErrorDuplicateItem) {
			return loadKeychainConnectionKey(service, account, false)
		}
		return nil, errors.New("Could not store the encryption key in macOS Keychain")
	}
	return key, nil
}
