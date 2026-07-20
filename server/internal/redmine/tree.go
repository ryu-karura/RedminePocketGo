package redmine

import "sort"

// ツリー化は純粋関数として実装する（DB・HTTP 依存なし。plan.md フェーズ 4）。
// Redmine の flat な parent.id 配列を Tabulator の dataTree が読める
// 入れ子構造へ変換する。親が結果セットに居ない項目（権限やフィルタで
// 親が見えない）はルート扱い。循環参照（データ破損）でもノードを失わない。

// ProjectNode はプロジェクトツリーの 1 ノード。
type ProjectNode struct {
	Project
	Children []*ProjectNode `json:"children,omitempty"`
}

// IssueNode はチケットツリーの 1 ノード。
type IssueNode struct {
	Issue
	Children []*IssueNode `json:"children,omitempty"`
}

// linkTree は要素 i の親子関係を index で解決する。
// 返り値: ルートの index 列と、index ごとの子 index 列。
func linkTree(n int, id func(i int) int, parentID func(i int) (int, bool)) (roots []int, children [][]int) {
	children = make([][]int, n)
	byID := make(map[int]int, n) // Redmine ID → index
	for i := 0; i < n; i++ {
		byID[id(i)] = i
	}

	hasParent := make([]bool, n)
	for i := 0; i < n; i++ {
		if pid, ok := parentID(i); ok {
			if pi, found := byID[pid]; found && pi != i {
				children[pi] = append(children[pi], i)
				hasParent[i] = true
				continue
			}
		}
	}
	for i := 0; i < n; i++ {
		if !hasParent[i] {
			roots = append(roots, i)
		}
	}

	// 循環（全員が親持ちの閉路）で到達不能なノードを失わないよう、
	// 到達済みを数えて、残りがあれば代表 1 つをルートへ昇格して繰り返す。
	reached := make([]bool, n)
	var visit func(i int)
	visit = func(i int) {
		if reached[i] {
			return
		}
		reached[i] = true
		for _, c := range children[i] {
			visit(c)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	for {
		promote := -1
		for i := 0; i < n; i++ {
			if !reached[i] && (promote == -1 || id(i) < id(promote)) {
				promote = i
			}
		}
		if promote == -1 {
			return roots, children
		}
		// 昇格するノードを親の子リストから外す
		for pi := range children {
			for k, c := range children[pi] {
				if c == promote {
					children[pi] = append(children[pi][:k], children[pi][k+1:]...)
					break
				}
			}
		}
		roots = append(roots, promote)
		visit(promote)
	}
}

// BuildProjectTree はプロジェクトを親子構造に整形する（名前順で安定）。
func BuildProjectTree(projects []Project) []*ProjectNode {
	nodes := make([]*ProjectNode, len(projects))
	for i, p := range projects {
		nodes[i] = &ProjectNode{Project: p}
	}
	roots, children := linkTree(len(projects),
		func(i int) int { return projects[i].ID },
		func(i int) (int, bool) {
			if projects[i].Parent == nil {
				return 0, false
			}
			return projects[i].Parent.ID, true
		})

	for i, cs := range children {
		for _, c := range cs {
			nodes[i].Children = append(nodes[i].Children, nodes[c])
		}
	}
	out := make([]*ProjectNode, 0, len(roots))
	for _, r := range roots {
		out = append(out, nodes[r])
	}
	var sortNodes func(ns []*ProjectNode)
	sortNodes = func(ns []*ProjectNode) {
		sort.Slice(ns, func(a, b int) bool { return ns[a].Name < ns[b].Name })
		for _, n := range ns {
			sortNodes(n.Children)
		}
	}
	sortNodes(out)
	return out
}

// BuildIssueTree はチケットを親子構造に整形する（ID 順で安定）。
func BuildIssueTree(issues []Issue) []*IssueNode {
	nodes := make([]*IssueNode, len(issues))
	for i, is := range issues {
		nodes[i] = &IssueNode{Issue: is}
	}
	roots, children := linkTree(len(issues),
		func(i int) int { return issues[i].ID },
		func(i int) (int, bool) {
			if issues[i].Parent == nil {
				return 0, false
			}
			return issues[i].Parent.ID, true
		})

	for i, cs := range children {
		for _, c := range cs {
			nodes[i].Children = append(nodes[i].Children, nodes[c])
		}
	}
	out := make([]*IssueNode, 0, len(roots))
	for _, r := range roots {
		out = append(out, nodes[r])
	}
	var sortNodes func(ns []*IssueNode)
	sortNodes = func(ns []*IssueNode) {
		sort.Slice(ns, func(a, b int) bool { return ns[a].ID < ns[b].ID })
		for _, n := range ns {
			sortNodes(n.Children)
		}
	}
	sortNodes(out)
	return out
}
