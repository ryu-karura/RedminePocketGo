package redmine

import "testing"

func ref(id int) *Ref { return &Ref{ID: id} }

func TestBuildProjectTree(t *testing.T) {
	projects := []Project{
		{ID: 3, Name: "c-child", Parent: ref(1)},
		{ID: 1, Name: "a-root"},
		{ID: 2, Name: "b-root"},
		{ID: 4, Name: "d-grandchild", Parent: ref(3)},
		{ID: 5, Name: "e-orphan", Parent: ref(99)}, // 親が見えない → ルート扱い
	}
	roots := BuildProjectTree(projects)

	if len(roots) != 3 {
		t.Fatalf("roots = %d; want 3 (a, b, orphan)", len(roots))
	}
	// 名前順で安定
	if roots[0].Name != "a-root" || roots[1].Name != "b-root" || roots[2].Name != "e-orphan" {
		t.Errorf("root order: %s, %s, %s", roots[0].Name, roots[1].Name, roots[2].Name)
	}
	a := roots[0]
	if len(a.Children) != 1 || a.Children[0].Name != "c-child" {
		t.Fatalf("a children = %+v", a.Children)
	}
	if len(a.Children[0].Children) != 1 || a.Children[0].Children[0].Name != "d-grandchild" {
		t.Errorf("grandchild missing: %+v", a.Children[0].Children)
	}
}

func TestBuildIssueTree(t *testing.T) {
	parent := func(id int) *struct {
		ID int `json:"id"`
	} {
		return &struct {
			ID int `json:"id"`
		}{ID: id}
	}
	issues := []Issue{
		{ID: 10, Subject: "root-b"},
		{ID: 11, Subject: "child", Parent: parent(10)},
		{ID: 9, Subject: "root-a"},
		{ID: 12, Subject: "orphan", Parent: parent(999)},
	}
	roots := BuildIssueTree(issues)
	if len(roots) != 3 {
		t.Fatalf("roots = %d; want 3", len(roots))
	}
	// チケットは ID 順で安定
	if roots[0].ID != 9 || roots[1].ID != 10 || roots[2].ID != 12 {
		t.Errorf("order: %d, %d, %d", roots[0].ID, roots[1].ID, roots[2].ID)
	}
	if len(roots[1].Children) != 1 || roots[1].Children[0].ID != 11 {
		t.Errorf("children of 10: %+v", roots[1].Children)
	}
}

func TestBuildTreeEmptyAndCycle(t *testing.T) {
	if got := BuildProjectTree(nil); len(got) != 0 {
		t.Errorf("nil input: %v", got)
	}
	// 相互参照（データ破損）でも無限ループせず、全ノードがどこかに現れる
	cyc := []Project{
		{ID: 1, Name: "x", Parent: ref(2)},
		{ID: 2, Name: "y", Parent: ref(1)},
	}
	roots := BuildProjectTree(cyc)
	total := 0
	var count func(ns []*ProjectNode)
	count = func(ns []*ProjectNode) {
		for _, n := range ns {
			total++
			count(n.Children)
		}
	}
	count(roots)
	if total != 2 {
		t.Errorf("cycle handling lost nodes: total = %d; want 2", total)
	}
}
