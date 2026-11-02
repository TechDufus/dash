package overview

import "github.com/TechDufus/dash/internal/model"

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

// worst returns the most severe health; no arguments is ok.
func worst(hs ...model.Health) model.Health {
	w := model.HealthOK
	for _, h := range hs {
		if rank(h) > rank(w) {
			w = h
		}
	}
	return w
}
