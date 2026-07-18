//go:build !linux && !darwin

package eebusfacade

import (
	"context"
	"errors"
)

func loadNativeProtectedRuntimeMaterial(context.Context, string) (runtimeMaterial, error) {
	return runtimeMaterial{}, errors.New("protected runtime material: platform unavailable")
}
