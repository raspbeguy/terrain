package widgets

import (
	"reflect"
	"sort"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func stateOf(resources ...*tfjson.StateResource) *tfjson.State {
	return &tfjson.State{
		Values: &tfjson.StateValues{
			RootModule: &tfjson.StateModule{
				Resources: resources,
			},
		},
	}
}

func resource(addr, provider string, attrs map[string]any) *tfjson.StateResource {
	return &tfjson.StateResource{
		Address:         addr,
		ProviderName:    provider,
		AttributeValues: attrs,
	}
}

func TestDiffStates_BothEmpty(t *testing.T) {
	t.Parallel()
	a := stateOf()
	b := stateOf()
	added, changed, removed := diffStates(a, b)
	if len(added)+len(changed)+len(removed) != 0 {
		t.Errorf("expected nothing, got +%v ~%v -%v", added, changed, removed)
	}
}

func TestDiffStates_AddedOnly(t *testing.T) {
	t.Parallel()
	a := stateOf()
	b := stateOf(
		resource("aws_instance.web", "aws", map[string]any{"id": "i-123"}),
		resource("null_resource.demo", "null", map[string]any{"id": "abc"}),
	)
	added, changed, removed := diffStates(a, b)
	if len(added) != 2 || len(changed) != 0 || len(removed) != 0 {
		t.Fatalf("unexpected: +%d ~%d -%d", len(added), len(changed), len(removed))
	}
	addrs := []string{added[0].Address, added[1].Address}
	sort.Strings(addrs)
	want := []string{"aws_instance.web", "null_resource.demo"}
	if !reflect.DeepEqual(addrs, want) {
		t.Errorf("addresses: %v vs %v", addrs, want)
	}
}

func TestDiffStates_RemovedOnly(t *testing.T) {
	t.Parallel()
	a := stateOf(resource("aws_s3_bucket.gone", "aws", map[string]any{"id": "x"}))
	b := stateOf()
	added, changed, removed := diffStates(a, b)
	if len(added) != 0 || len(changed) != 0 || len(removed) != 1 {
		t.Fatalf("unexpected: +%d ~%d -%d", len(added), len(changed), len(removed))
	}
	if removed[0].Address != "aws_s3_bucket.gone" {
		t.Errorf("address: %v", removed[0].Address)
	}
}

func TestDiffStates_NoOpWhenIdentical(t *testing.T) {
	t.Parallel()
	attrs := map[string]any{"id": "i-1", "size": "t2.micro"}
	a := stateOf(resource("aws_instance.x", "aws", attrs))
	b := stateOf(resource("aws_instance.x", "aws", attrs))
	added, changed, removed := diffStates(a, b)
	if len(added)+len(changed)+len(removed) != 0 {
		t.Errorf("expected no diffs, got +%d ~%d -%d", len(added), len(changed), len(removed))
	}
}

func TestDiffStates_ChangedAttributes(t *testing.T) {
	t.Parallel()
	a := stateOf(resource("aws_instance.x", "aws", map[string]any{
		"id":   "i-1",
		"size": "t2.micro",
		"tag":  "old",
	}))
	b := stateOf(resource("aws_instance.x", "aws", map[string]any{
		"id":     "i-1",
		"size":   "t2.large",
		"tag":    "old",
		"region": "us-east-1",
	}))
	added, changed, removed := diffStates(a, b)
	if len(added) != 0 || len(removed) != 0 || len(changed) != 1 {
		t.Fatalf("unexpected: +%d ~%d -%d", len(added), len(changed), len(removed))
	}
	got := changed[0]
	if got.Address != "aws_instance.x" {
		t.Errorf("address: %v", got.Address)
	}
	keys := make(map[string]bool)
	for _, c := range got.AttrChanges {
		keys[c.Key] = true
	}
	if len(keys) != 2 || !keys["size"] || !keys["region"] {
		t.Errorf("attribute keys: %v", keys)
	}

	for _, c := range got.AttrChanges {
		if c.Key == "region" {
			if c.FromExists || !c.ToExists {
				t.Errorf("region: FromExists=%v ToExists=%v", c.FromExists, c.ToExists)
			}
		}
	}
}

func TestDiffStates_ChildModuleResources(t *testing.T) {
	t.Parallel()
	makeNested := func(addrs ...string) *tfjson.State {
		var resources []*tfjson.StateResource
		for _, a := range addrs {
			resources = append(resources, resource(a, "aws", map[string]any{}))
		}
		return &tfjson.State{
			Values: &tfjson.StateValues{
				RootModule: &tfjson.StateModule{
					ChildModules: []*tfjson.StateModule{
						{Address: "module.network", Resources: resources},
					},
				},
			},
		}
	}
	a := makeNested("module.network.aws_vpc.main")
	b := makeNested("module.network.aws_vpc.main", "module.network.aws_subnet.public")
	added, changed, removed := diffStates(a, b)
	if len(added) != 1 || len(changed) != 0 || len(removed) != 0 {
		t.Fatalf("expected 1 added in child module: +%d ~%d -%d", len(added), len(changed), len(removed))
	}
	if added[0].Address != "module.network.aws_subnet.public" {
		t.Errorf("address: %v", added[0].Address)
	}
}

func TestDiffStates_BothNilTolerated(t *testing.T) {
	t.Parallel()
	added, changed, removed := diffStates(nil, nil)
	if len(added)+len(changed)+len(removed) != 0 {
		t.Errorf("expected nothing for nil/nil, got +%v ~%v -%v", added, changed, removed)
	}
}

func TestDiffStates_OneNil(t *testing.T) {
	t.Parallel()
	b := stateOf(resource("aws_instance.x", "aws", nil))
	added, _, removed := diffStates(nil, b)
	if len(added) != 1 || len(removed) != 0 {
		t.Errorf("nil from + populated to: expected 1 added, got +%d -%d", len(added), len(removed))
	}
}
