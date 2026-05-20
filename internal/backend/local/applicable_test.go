package local

import (
	"reflect"
	"testing"

	"github.com/raspbeguy/terrain/internal/domain"
)

func TestApplicableSets_GlobalAlwaysApplies(t *testing.T) {
	t.Parallel()
	all := []domain.VariableSet{
		{ID: "a", Name: "globalA", Scope: domain.ScopeGlobal},
		{ID: "b", Name: "globalB", Scope: domain.ScopeGlobal},
	}
	got := applicableSets(all, domain.Workspace{ID: "ws", ProjectID: "p"})
	if len(got) != 2 {
		t.Fatalf("expected 2 globals, got %d", len(got))
	}
}

func TestApplicableSets_ProjectScopeFilter(t *testing.T) {
	t.Parallel()
	all := []domain.VariableSet{
		{ID: "p1", Scope: domain.ScopeProject, ProjectID: "p-1"},
		{ID: "p2", Scope: domain.ScopeProject, ProjectID: "p-2"},
		{ID: "p3", Scope: domain.ScopeProject, ProjectID: ""},
	}
	got := applicableSets(all, domain.Workspace{ID: "ws", ProjectID: "p-1"})
	if len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("expected only p1, got %v", got)
	}

	got = applicableSets(all, domain.Workspace{ID: "ws"})
	if len(got) != 0 {
		t.Errorf("expected no matches for empty ProjectID, got %v", got)
	}
}

func TestApplicableSets_WorkspaceScopeFilter(t *testing.T) {
	t.Parallel()
	all := []domain.VariableSet{
		{ID: "w1", Scope: domain.ScopeWorkspace, Workspaces: []string{"ws-A", "ws-B"}},
		{ID: "w2", Scope: domain.ScopeWorkspace, Workspaces: []string{"ws-C"}},
	}
	got := applicableSets(all, domain.Workspace{ID: "ws-A"})
	if len(got) != 1 || got[0].ID != "w1" {
		t.Errorf("expected only w1, got %v", got)
	}
}

func TestApplicableSets_PrecedenceOrder(t *testing.T) {
	t.Parallel()
	all := []domain.VariableSet{
		{ID: "g", Scope: domain.ScopeGlobal},
		{ID: "p", Scope: domain.ScopeProject, ProjectID: "p-1"},
		{ID: "w", Scope: domain.ScopeWorkspace, Workspaces: []string{"ws-1"}},
	}
	got := applicableSets(all, domain.Workspace{ID: "ws-1", ProjectID: "p-1"})
	if len(got) != 3 {
		t.Fatalf("expected 3 applicable, got %d", len(got))
	}
	wantOrder := []string{"g", "p", "w"}
	gotOrder := []string{got[0].ID, got[1].ID, got[2].ID}
	if !reflect.DeepEqual(wantOrder, gotOrder) {
		t.Errorf("order mismatch: want %v, got %v", wantOrder, gotOrder)
	}
}

func TestApplicableSets_PriorityWithinScope(t *testing.T) {
	t.Parallel()
	all := []domain.VariableSet{
		{ID: "low", Scope: domain.ScopeGlobal, Priority: 1},
		{ID: "high", Scope: domain.ScopeGlobal, Priority: 10},
		{ID: "mid", Scope: domain.ScopeGlobal, Priority: 5},
	}
	got := applicableSets(all, domain.Workspace{ID: "ws"})
	gotOrder := []string{got[0].ID, got[1].ID, got[2].ID}
	want := []string{"low", "mid", "high"}
	if !reflect.DeepEqual(gotOrder, want) {
		t.Errorf("priority order: want %v, got %v", want, gotOrder)
	}
}

func TestApplicableSets_EmptyInput(t *testing.T) {
	t.Parallel()
	got := applicableSets(nil, domain.Workspace{ID: "ws"})
	if len(got) != 0 {
		t.Errorf("expected empty for nil input, got %v", got)
	}
}
