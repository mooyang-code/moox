package privatenet

import (
	"context"
	"fmt"
	"strings"
)

type Probe struct {
	From     string `json:"from"`
	To       string `json:"to"`
	PublicIP string `json:"public_ip,omitempty"`
	Port     string `json:"port,omitempty"`
	Area     string `json:"area,omitempty"`
	Expect   string `json:"expect,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

func listenerPorts(host ResolvedHost, eventBusPort string) []string {
	ports := make([]string, 0, 4)
	if hostHasRole(host.HostTarget, "storage") || hostHasRole(host.HostTarget, "view") {
		ports = append(ports, "11003", "11012")
	}
	if hostHasRole(host.HostTarget, "control") && eventBusPort != "" {
		ports = append(ports, eventBusPort)
	}
	return ports
}

func PlannedProbes(hosts []ResolvedHost, eventBusPort string) []Probe {
	out := make([]Probe, 0)
	for _, from := range hosts {
		for _, to := range hosts {
			if from.Address == to.Address || strings.TrimSpace(to.Address) == "" {
				continue
			}
			for _, port := range listenerPorts(to, eventBusPort) {
				out = append(out, Probe{
					From: from.Name, To: to.Name, PublicIP: to.Address, Port: port,
					Area: from.Area + "->" + to.Area, Expect: "open",
				})
			}
		}
	}
	return out
}

func RunHostProbes(ctx context.Context, exec func(context.Context, string, string) (string, error), hosts []ResolvedHost, probes []Probe) []Probe {
	byName := map[string]string{}
	for _, host := range hosts {
		byName[host.Name] = host.Address
	}
	for i, probe := range probes {
		fromIP := byName[probe.From]
		if fromIP == "" || exec == nil {
			probes[i].Status = "skipped"
			continue
		}
		script := fmt.Sprintf(`if timeout 5 bash -c "true >/dev/tcp/%s/%s"; then echo open; else echo closed; fi`, probe.PublicIP, probe.Port)
		stdout, err := exec(ctx, fromIP, script)
		stdout = strings.TrimSpace(stdout)
		if err == nil && strings.HasPrefix(stdout, "open") {
			probes[i].Status = "open"
			continue
		}
		probes[i].Status = "closed"
		probes[i].Detail = firstNonEmpty(stdout, fmt.Sprintf("%v", err))
	}
	return probes
}

func PublicProbesFailed(probes []Probe) error {
	failed := make([]string, 0)
	for _, probe := range probes {
		if probe.Expect != "open" {
			continue
		}
		if probe.Status != "open" {
			failed = append(failed, fmt.Sprintf("%s->%s:%s %s", probe.From, probe.To, probe.Port, firstNonEmpty(probe.Detail, probe.Status)))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("public-network probe failed: %s", strings.Join(failed, "; "))
}
