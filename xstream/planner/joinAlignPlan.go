package planner

import "github.com/emqx/kuiper/xsql"

type JoinAlignPlan struct {
	baseLogicalPlan
	Emitters []string
}

func (p JoinAlignPlan) Init() *JoinAlignPlan {
	p.baseLogicalPlan.self = &p
	return &p
}

// Push down to table first, then push to window
func (p *JoinAlignPlan) PushDownPredicate(condition xsql.Expr) (xsql.Expr, LogicalPlan) {
	if len(p.children) == 0 {
		return condition, p.self
	}
	rest := condition
	for i, child := range p.children {
		if _, ok := child.(*DataSourcePlan); ok {
			var newChild LogicalPlan
			rest, newChild = child.PushDownPredicate(rest)
			p.children[i] = newChild
		}
	}
	for i, child := range p.children {
		if _, ok := child.(*DataSourcePlan); !ok {
			var newChild LogicalPlan
			rest, newChild = child.PushDownPredicate(rest)
			p.children[i] = newChild
		}
	}
	return rest, p.self
}
