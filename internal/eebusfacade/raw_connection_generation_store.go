package eebusfacade

const (
	rawConnectionGenerationStateFilename = "raw-connection-generations-v1.json"
	rawConnectionGenerationLockFilename  = "raw-connection-generations-v1.lock"
	rawConnectionGenerationTempFilename  = ".raw-connection-generations-v1.tmp"
)

type rawConnectionGenerationStore interface {
	advance(runtimeEpoch uint64, ski string, generation uint64) error
}

type rawConnectionGenerationAllocator interface {
	allocateNext(runtimeEpoch uint64, ski string) (uint64, error)
}

type rawConnectionGenerationStoreFunc func(uint64, string, uint64) error

func (fn rawConnectionGenerationStoreFunc) advance(runtimeEpoch uint64, ski string, generation uint64) error {
	if fn == nil {
		return nil
	}
	return fn(runtimeEpoch, ski, generation)
}

type rawConnectionGenerationState struct {
	RuntimeEpoch uint64            `json:"runtime_epoch"`
	Generations  map[string]uint64 `json:"generations"`
}
