package peer

import (
	"errors"
	"reflect"
	"sync"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

var (
	errCEVCUnbound   = errors.New("peer: the client feature is not bound to the remote server feature")
	errCEVCNoMatch   = errors.New("peer: the selector does not name this instance, remote feature or function")
	errCEVCRejected  = errors.New("peer: the selector is not the writable description")
	errCEVCAbsent    = errors.New("peer: no description matches the selector")
	errCEVCAmbiguous = errors.New("peer: more than one description matches the selector")
	errCEVCNoData    = errors.New("peer: the selected data is not held")
	errCEVCNoSlot    = errors.New("peer: the selected series does not hold the slot")
)

type cevcResults struct {
	mu    sync.Mutex
	items map[model.MsgCounterType]model.ResultDataType
}

func newCEVCResults() *cevcResults {
	return &cevcResults{items: map[model.MsgCounterType]model.ResultDataType{}}
}

func (r *cevcResults) note(msg spineapi.ResponseMessage) {
	result, ok := msg.Data.(*model.ResultDataType)
	if !ok || result == nil {
		return
	}
	r.mu.Lock()
	r.items[msg.MsgCounterReference] = *result
	r.mu.Unlock()
}

func (r *cevcResults) get(counter model.MsgCounterType) (model.ResultDataType, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, ok := r.items[counter]
	return result, ok
}

func cevcIdentity(key string, remote spineapi.EntityRemoteInterface, instance string, device model.AddressDeviceType, entity []model.AddressEntityType, feature model.FeatureTypeType, function model.FunctionType, wantFeature model.FeatureTypeType, wantFunction model.FunctionType) error {
	address := remote.Device().Address()
	if instance != key || address == nil || device != *address || !reflect.DeepEqual(entity, remote.Address().Entity) || feature != wantFeature || function != wantFunction {
		return errCEVCNoMatch
	}
	return nil
}
