package hardware

import "github.com/TechDufus/dash/internal/model"

// tempHealth is the CPU package temperature rule: from crit up is critical, from warn up is a
// warning, whatever is below is ok. The thresholds are plugins.hardware.tempThresholds; every
// health the plugin shows (the host, the KPI, the topology host, the attention item) comes from
// this one function.
func tempHealth(warn, crit int, c float64) model.Health {
	switch {
	case c >= float64(crit):
		return model.HealthCrit
	case c >= float64(warn):
		return model.HealthWarn
	}
	return model.HealthOK
}

func rank(h model.Health) int {
	switch h {
	case model.HealthCrit:
		return 3
	case model.HealthWarn:
		return 2
	case model.HealthUnknown:
		return 1
	}
	return 0
}

// worst is the most severe health; no arguments is ok.
func worst(hs ...model.Health) model.Health {
	w := model.HealthOK
	for _, h := range hs {
		if rank(h) > rank(w) {
			w = h
		}
	}
	return w
}

// maxOf is the largest of vs, 0 for none.
func maxOf(vs []float64) float64 {
	m := 0.0
	for _, v := range vs {
		m = max(m, v)
	}
	return m
}
