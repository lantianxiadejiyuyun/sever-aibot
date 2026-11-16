// Package asset resolves an asset's jump chain without depending on the
// database. Callers inject a Lookup so the resolver stays easy to test and
// reusable by the terminal/data-source code paths.
package asset

import (
	"context"
	"errors"
	"fmt"
)

// MaxHops is the longest allowed via-asset chain. A direct asset has zero
// hops; v1 allows at most three jump assets between the entry and the target.
const MaxHops = 3

var (
	// ErrNotFound means the target or one of its jump assets is missing.
	ErrNotFound = errors.New("asset not found")
	// ErrCycle means the via-asset chain references an asset twice.
	ErrCycle = errors.New("asset route cycle detected")
	// ErrTooManyHops means the chain exceeds MaxHops.
	ErrTooManyHops = errors.New("asset route has too many hops")
)

// Node is the subset of an asset that route resolution needs.
type Node struct {
	ID          string
	Name        string
	Address     string
	Port        int
	Protocol    string
	ViaDeviceID string
	ViaAssetID  string
}

// Lookup loads one asset by ID. Missing assets must return ErrNotFound.
type Lookup interface {
	GetAsset(ctx context.Context, id string) (Node, error)
}

// Hop is one jump asset between the entry point and the target.
type Hop struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// Endpoint is the terminal asset address.
type Endpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// Route is the resolved path: jump assets ordered from the entry side toward
// the target, plus the target endpoint and the agent device used to enter the
// chain (empty means the control plane connects directly).
type Route struct {
	Hops     []Hop    `json:"hops"`
	Endpoint Endpoint `json:"endpoint"`
	DeviceID string   `json:"viaDeviceId,omitempty"`
}

// HopIDs returns the ordered jump-asset IDs.
func (r *Route) HopIDs() []string {
	ids := make([]string, 0, len(r.Hops))
	for _, h := range r.Hops {
		ids = append(ids, h.ID)
	}
	return ids
}

// Resolve walks via_asset_id links from the target. It rejects cycles and
// chains longer than MaxHops with errors that identify the offending asset.
func Resolve(ctx context.Context, lookup Lookup, id string) (*Route, error) {
	if lookup == nil {
		return nil, errors.New("asset: nil lookup")
	}
	if id == "" {
		return nil, fmt.Errorf("%w: empty asset id", ErrNotFound)
	}
	target, err := lookup.GetAsset(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, err
	}

	chain := []Node{target}
	seen := map[string]bool{target.ID: true}
	current := target
	for current.ViaAssetID != "" {
		if len(chain)-1 >= MaxHops {
			return nil, fmt.Errorf("%w: route from %q exceeds %d hops", ErrTooManyHops, id, MaxHops)
		}
		if seen[current.ViaAssetID] {
			return nil, fmt.Errorf("%w: %q appears twice in the route", ErrCycle, current.ViaAssetID)
		}
		next, err := lookup.GetAsset(ctx, current.ViaAssetID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, fmt.Errorf("%w: jump asset %q", ErrNotFound, current.ViaAssetID)
			}
			return nil, err
		}
		if seen[next.ID] {
			return nil, fmt.Errorf("%w: %q appears twice in the route", ErrCycle, next.ID)
		}
		seen[next.ID] = true
		chain = append(chain, next)
		current = next
	}

	// chain is target-first; reverse the jump portion so hops read from the
	// entry side toward the target (the form ProxyJump expects).
	hops := make([]Hop, 0, len(chain)-1)
	for i := len(chain) - 1; i >= 1; i-- {
		n := chain[i]
		hops = append(hops, Hop{ID: n.ID, Name: n.Name, Address: n.Address, Port: n.Port})
	}
	return &Route{
		Hops:     hops,
		Endpoint: Endpoint{Address: target.Address, Port: target.Port},
		DeviceID: current.ViaDeviceID,
	}, nil
}
