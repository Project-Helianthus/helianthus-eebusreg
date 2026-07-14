//go:build darwin && !cgo

package eebusstore

import "errors"

func inspectAdditionalAccess(int) (bool, error) {
	return false, errors.New("Darwin ACL inspection requires cgo")
}
