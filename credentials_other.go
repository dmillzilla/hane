//go:build !linux

package main

import "github.com/zalando/go-keyring"

func nativeCredentialGet(key string) (string, error) { return keyring.Get(credentialService, key) }
func nativeCredentialSet(key, password string) error {
	return keyring.Set(credentialService, key, password)
}
