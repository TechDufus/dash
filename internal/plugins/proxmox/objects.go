package proxmox

import (
	"context"
	"strconv"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
)

// maxTasks bounds the recent tasks a VM's context lists.
const maxTasks = 5

// Context implements plugin.Objects. A VM adds its charts, its recent tasks, the node it runs on
// and the Kubernetes node it is; the twin, which embeds the plugin, answers the same way.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	part, obj, err := p.Part(ref)
	if err != nil {
		return part, err
	}
	vm, ok := obj.(model.VM)
	if !ok {
		return part, nil
	}
	id := strconv.Itoa(vm.VMID)
	args := map[string]string{"vmid": id}
	for _, name := range []string{"vm.cpu", "vm.mem", "vm.netIn", "vm.netOut", "vm.diskRead", "vm.diskWrite"} {
		part.Series = append(part.Series, model.SeriesRef{Name: name, Args: args})
	}
	if v, ok := p.Latest(topicVMs); ok {
		d, _ := v.(model.VMsData)
		n := 0
		for _, t := range d.Tasks {
			if t.ID != id || n == maxTasks {
				continue
			}
			n++
			part.Facts = append(part.Facts, model.ContextFact{
				Name:  "task." + strconv.Itoa(n),
				Value: t.Type + " by " + t.User + ": " + t.Status + " (started " + t.StartTime.UTC().Format("2006-01-02T15:04:05Z") + ")",
			})
		}
	}
	if v, ok := p.Latest(topicNode); ok {
		if d, ok := v.(pvemodel.NodeData); ok && d.Ref != "" {
			part.Related = append(part.Related, d.Ref)
		}
	}
	if vm.K8sNode != nil && vm.K8sNode.Name != "" {
		part.Related = append(part.Related, "kubernetes:Node/"+vm.K8sNode.Name)
	}
	return part, nil
}

var _ plugin.Objects = (*Plugin)(nil)
