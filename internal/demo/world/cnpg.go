package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// CNPG is the cnpg.clusters payload: one healthy CloudNativePG cluster with a recent backup. The
// clusters carry no ref; the twin that publishes them adds it.
func (w *World) CNPG() model.CNPGClustersData { return w.cnpg(w.clock()) }

func (w *World) cnpg(now time.Time) model.CNPGClustersData {
	return model.CNPGClustersData{Clusters: []model.CNPGCluster{{
		Namespace: "acme", Name: "acme-db", Instances: 2, ReadyInstances: 2,
		Phase:                "Cluster in healthy state",
		LastBackupAgeSeconds: new(Around("cnpgbk", now, 5.2*3600, 1800)),
		Links:                w.links.Headlamp("Cluster", "acme", "acme-db"),
	}}}
}
