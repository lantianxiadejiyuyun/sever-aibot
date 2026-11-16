package asset

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeLookup map[string]Node

func (f fakeLookup) GetAsset(_ context.Context, id string) (Node, error) {
	n, ok := f[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	return n, nil
}

func node(id, via, device string) Node {
	return Node{
		ID:          id,
		Name:        "name-" + id,
		Address:     "10.0.0." + id,
		Port:        22,
		Protocol:    "ssh",
		ViaDeviceID: device,
		ViaAssetID:  via,
	}
}

func TestResolveDirect(t *testing.T) {
	lookup := fakeLookup{"a": node("a", "", "")}
	route, err := Resolve(context.Background(), lookup, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(route.Hops) != 0 {
		t.Fatalf("hops = %v, want none", route.Hops)
	}
	if route.Endpoint.Address != "10.0.0.a" || route.Endpoint.Port != 22 {
		t.Fatalf("endpoint = %+v", route.Endpoint)
	}
	if route.DeviceID != "" {
		t.Fatalf("device = %q, want empty", route.DeviceID)
	}
}

func TestResolveDirectViaDevice(t *testing.T) {
	lookup := fakeLookup{"a": node("a", "", "dev_1")}
	route, err := Resolve(context.Background(), lookup, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(route.Hops) != 0 {
		t.Fatalf("hops = %v, want none", route.Hops)
	}
	if route.DeviceID != "dev_1" {
		t.Fatalf("device = %q, want dev_1", route.DeviceID)
	}
}

func TestResolveOneHop(t *testing.T) {
	lookup := fakeLookup{
		"a": node("a", "b", ""),
		"b": node("b", "", "dev_entry"),
	}
	route, err := Resolve(context.Background(), lookup, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := route.HopIDs(), []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hops = %v, want %v", got, want)
	}
	if route.DeviceID != "dev_entry" {
		t.Fatalf("device = %q, want dev_entry", route.DeviceID)
	}
	if route.Endpoint.Address != "10.0.0.a" {
		t.Fatalf("endpoint = %+v", route.Endpoint)
	}
}

func TestResolveTwoHops(t *testing.T) {
	lookup := fakeLookup{
		"a": node("a", "b", ""),
		"b": node("b", "c", ""),
		"c": node("c", "", "dev_entry"),
	}
	route, err := Resolve(context.Background(), lookup, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := route.HopIDs(), []string{"c", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hops = %v, want %v (entry first)", got, want)
	}
}

func TestResolveThreeHopsAllowed(t *testing.T) {
	lookup := fakeLookup{
		"a": node("a", "b", ""),
		"b": node("b", "c", ""),
		"c": node("c", "d", ""),
		"d": node("d", "", "dev_entry"),
	}
	route, err := Resolve(context.Background(), lookup, "a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := route.HopIDs(), []string{"d", "c", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hops = %v, want %v", got, want)
	}
}

func TestResolveFourHopsRejected(t *testing.T) {
	lookup := fakeLookup{
		"a": node("a", "b", ""),
		"b": node("b", "c", ""),
		"c": node("c", "d", ""),
		"d": node("d", "e", ""),
		"e": node("e", "", "dev_entry"),
	}
	if _, err := Resolve(context.Background(), lookup, "a"); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("err = %v, want ErrTooManyHops", err)
	}
}

func TestResolveCycleRejected(t *testing.T) {
	lookup := fakeLookup{
		"a": node("a", "b", ""),
		"b": node("b", "c", ""),
		"c": node("c", "b", ""),
	}
	if _, err := Resolve(context.Background(), lookup, "a"); !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
}

func TestResolveSelfCycleRejected(t *testing.T) {
	lookup := fakeLookup{"a": node("a", "a", "")}
	if _, err := Resolve(context.Background(), lookup, "a"); !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
}

func TestResolveMissingTargetRejected(t *testing.T) {
	if _, err := Resolve(context.Background(), fakeLookup{}, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveMissingJumpRejected(t *testing.T) {
	lookup := fakeLookup{"a": node("a", "missing", "")}
	if _, err := Resolve(context.Background(), lookup, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestResolvePropagatesLookupError(t *testing.T) {
	want := errors.New("database down")
	lookup := errorLookup{err: want}
	if _, err := Resolve(context.Background(), lookup, "a"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

type errorLookup struct{ err error }

func (e errorLookup) GetAsset(context.Context, string) (Node, error) {
	return Node{}, e.err
}
