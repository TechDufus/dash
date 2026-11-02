package kubernetes

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// backupRow is one backup as the query reported it: the row's name and the seconds since the
// last successful run.
type backupRow struct {
	name string
	age  float64
}

// backupAgeQuery is the age of the last successful run of the CronJob named cronjob, or
// cronjob-<anything>, in namespace ns.
func backupAgeQuery(ns, cronjob string) string {
	return fmt.Sprintf(`time() - kube_cronjob_status_last_successful_time{namespace=%s,cronjob=~%s}`,
		strconv.Quote(ns), strconv.Quote(regexp.QuoteMeta(cronjob)+"(-.+)?"))
}

// backupQueryName is the name of the i-th backup job's query in a batch.
func backupQueryName(i int) string { return "backup/" + strconv.Itoa(i) }

// backupKey identifies a configured backup job in the snapshot.
func backupKey(j pluginconf.BackupJob) string {
	return j.Namespace + "\x00" + j.CronJob + "\x00" + j.RowName()
}

// backupRows reads the answer of one job's query. With a label the job is one row named by it,
// aged by the freshest of the matched CronJobs; without one every matched CronJob is a row
// named by its own name. A series that carries no cronjob name cannot be named and is dropped.
func backupRows(j pluginconf.BackupJob, samples []svc.Sample) []backupRow {
	if label := j.RowName(); label != "" {
		if len(samples) == 0 {
			return nil
		}
		age := samples[0].Value
		for _, s := range samples[1:] {
			age = min(age, s.Value)
		}
		return []backupRow{{name: label, age: age}}
	}
	byName := map[string]float64{}
	for _, s := range samples {
		if name := s.Labels["cronjob"]; name != "" {
			byName[name] = s.Value
		}
	}
	rows := make([]backupRow, 0, len(byName))
	for name, age := range byName {
		rows = append(rows, backupRow{name: name, age: age})
	}
	return rows
}

// backupJobsOf is the backup rows of every configured job, sorted by ref. Two jobs may report
// the same row (a CronJob named after another job's prefix); the row is then listed once, with
// the smaller age.
func backupJobsOf(cfg pluginconf.Kubernetes, s storageSnap) []model.BackupJob {
	byRef := map[string]model.BackupJob{}
	for _, j := range cfg.BackupJobs {
		for _, r := range s.backups[backupKey(j)] {
			ref := plugin.Ref(ID, "BackupJob", j.Namespace, r.name)
			if have, ok := byRef[ref]; ok && *have.LastSuccessAgeSeconds <= r.age {
				continue
			}
			byRef[ref] = model.BackupJob{Ref: ref, Name: r.name, LastSuccessAgeSeconds: new(r.age)}
		}
	}
	out := make([]model.BackupJob, 0, len(byRef))
	for _, b := range byRef {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}
