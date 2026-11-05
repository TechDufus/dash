package mcp

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
)

// maxOverviewAttention is the most attention items dash_overview returns (design N.2).
const maxOverviewAttention = 50

type overviewInput struct{}

// overviewResult is dash_overview's answer.
type overviewResult struct {
	Status    model.Health          `json:"status"`
	KPIs      []model.KPI           `json:"kpis"`
	Attention []model.AttentionItem `json:"attention"`
	// More is how many attention items were left out.
	More int `json:"more"`
}

// toolTable lists dash's tools in the order tools/list shows them.
func (s *Server) toolTable() []tool {
	return []tool{
		{
			name: "dash_overview", title: "Homelab overview", scope: "read", input: &overviewInput{},
			desc: "The homelab at a glance: the overall status, the key figures every plugin contributes, and at most " +
				"50 attention items (what needs the operator's attention), most severe first. Start here.",
			run: func(context.Context, caller, any) (any, string, error) {
				ov, ok := s.o.Backend.Overview()
				if !ok {
					return nil, "", refuse(model.ErrCodeUpstreamUnavailable, "the overview is not ready yet")
				}
				res := overviewResult{Status: ov.Status, KPIs: ov.KPIList, Attention: ov.Attention}
				if res.KPIs == nil {
					res.KPIs = []model.KPI{}
				}
				if res.Attention == nil {
					res.Attention = []model.AttentionItem{}
				}
				if n := len(res.Attention); n > maxOverviewAttention {
					res.Attention, res.More = res.Attention[:maxOverviewAttention], n-maxOverviewAttention
				}
				return res, "", nil
			},
		},
	}
}
