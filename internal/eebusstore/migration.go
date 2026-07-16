package eebusstore

import (
	"errors"
	"fmt"
	"sort"
)

type migrationEdge struct {
	from  uint64
	to    uint64
	apply func(stateV1) (stateV1, error)
}

type migrationGraph struct {
	current uint64
	edges   map[uint64]migrationEdge
}

func newMigrationGraph(current uint64, edges []migrationEdge) (migrationGraph, error) {
	graph := migrationGraph{current: current, edges: make(map[uint64]migrationEdge, len(edges))}
	if current == 0 {
		return migrationGraph{}, migrationGraphError("current version is zero")
	}
	ordered := append([]migrationEdge(nil), edges...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].from == ordered[j].from {
			return ordered[i].to < ordered[j].to
		}
		return ordered[i].from < ordered[j].from
	})
	seenTargets := make(map[uint64]struct{}, len(ordered))
	for _, edge := range ordered {
		if edge.from == 0 || edge.to != edge.from+1 || edge.to > current || edge.apply == nil {
			return migrationGraph{}, migrationGraphError("edge is not a forward adjacent version")
		}
		if _, exists := graph.edges[edge.from]; exists {
			return migrationGraph{}, migrationGraphError("branch or duplicate source")
		}
		if _, exists := seenTargets[edge.to]; exists {
			return migrationGraph{}, migrationGraphError("duplicate target")
		}
		graph.edges[edge.from] = edge
		seenTargets[edge.to] = struct{}{}
	}
	return graph, nil
}

func (graph migrationGraph) pathFrom(version uint64) ([]migrationEdge, error) {
	if version > graph.current {
		return nil, newStoreError(outcomeUnsupportedFutureVersion, "migration_path", errors.New("future version"))
	}
	if version == graph.current {
		return nil, nil
	}
	path := make([]migrationEdge, 0, graph.current-version)
	for cursor := version; cursor < graph.current; cursor++ {
		edge, exists := graph.edges[cursor]
		if !exists || edge.to != cursor+1 {
			return nil, newStoreError(outcomeUnsupportedLegacyVersion, "migration_path", errors.New("no unique path"))
		}
		path = append(path, edge)
	}
	return path, nil
}

func (graph migrationGraph) apply(version uint64, source stateV1) (stateV1, error) {
	path, err := graph.pathFrom(version)
	if err != nil {
		return stateV1{}, err
	}
	current := cloneStateV1(source)
	for _, edge := range path {
		next, err := edge.apply(cloneStateV1(current))
		if err != nil {
			return stateV1{}, newStoreError(outcomeMigrationFailed, "apply_migration", err)
		}
		if err := validateStateV1(next); err != nil {
			return stateV1{}, newStoreError(outcomeMigrationFailed, "validate_migration", err)
		}
		current = cloneStateV1(next)
	}
	return current, nil
}

func cloneStateV1(source stateV1) stateV1 {
	cloned := stateV1{remoteIdentities: make([]remoteIdentityV1, len(source.remoteIdentities))}
	if source.localIdentity != nil {
		identity := *source.localIdentity
		identity.certificateChainDER = make([][]byte, len(source.localIdentity.certificateChainDER))
		for index, certificate := range source.localIdentity.certificateChainDER {
			identity.certificateChainDER[index] = append([]byte(nil), certificate...)
		}
		identity.keyReference.sealedBlob = append([]byte(nil), source.localIdentity.keyReference.sealedBlob...)
		identity.localSKI = append([]byte(nil), source.localIdentity.localSKI...)
		cloned.localIdentity = &identity
	}
	for index, identity := range source.remoteIdentities {
		cloned.remoteIdentities[index] = identity
		cloned.remoteIdentities[index].recordID = append([]byte(nil), identity.recordID...)
		cloned.remoteIdentities[index].remoteSKI = append([]byte(nil), identity.remoteSKI...)
	}
	cloned.controlEnvelope = append([]byte(nil), source.controlEnvelope...)
	return cloned
}

func migrateMSP04BStateToMSP04C(source stateV1) (stateV1, error) {
	return cloneStateV1(source), nil
}

func currentMigrationGraph() (migrationGraph, error) {
	return newMigrationGraph(currentSchemaVersion, []migrationEdge{{
		from:  1,
		to:    2,
		apply: migrateMSP04BStateToMSP04C,
	}})
}

func migrationGraphError(reason string) *storeError {
	return newStoreError(outcomeMigrationFailed, "migration_graph", fmt.Errorf("%s", reason))
}
