//go:build !linux && !darwin

package eebusstore

import (
	"context"
	"crypto"
	"errors"
)

type KeyProvider interface {
	Probe(string, uint64) error
	Validate([]byte, []byte) error
	Unseal([]byte) (crypto.Signer, error)
}

type KeyProviderBinding struct {
	ID       string
	Version  uint64
	Provider KeyProvider
}

type AssociationBridge struct{}

func OpenAssociationBridge(string, []KeyProviderBinding) (*AssociationBridge, string) {
	return nil, "filesystem_capability_unavailable"
}

func (*AssociationBridge) Reload(context.Context) (uint64, map[string]string, string) {
	return 0, nil, "filesystem_capability_unavailable"
}

func (*AssociationBridge) SelectedGeneration() uint64 { return 0 }
func (*AssociationBridge) Commit(context.Context, uint64, []byte, string) string {
	return "commit_not_published"
}
func (*AssociationBridge) Close() error { return errors.New("filesystem_capability_unavailable") }
