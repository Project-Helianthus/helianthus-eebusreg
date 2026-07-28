//go:build !linux && !darwin

package eebusfacade

import "errors"

func newRawConnectionGenerationStore(string) (rawConnectionGenerationStore, error) {
	return nil, errors.New("raw connection generation persistence is unavailable")
}
