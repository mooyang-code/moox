package privatenet

import (
	"context"
	"fmt"
	"strings"
)

type Probe struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	PublicIP   string   `json:"public_ip,omitempty"`
	Port       string   `json:"port,omitempty"`
	Area       string   `json:"area,omitempty"`
	Expect     string   `json:"expect,omitempty"`
	Status     string   `json:"status"`
	Detail     string   `json:"detail,omitempty"`
	ConfigHits []string `json:"config_hits,omitempty"`
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

func InspectPublicIPRefs(ctx context.Context, exec func(context.Context, string, string) (string, error), hosts []ResolvedHost) []Probe {
	out := make([]Probe, 0, len(hosts))
	script := `grep -RIn --include='*.yaml' --include='*.yml' --include='*.env' --include='*.sh' --include='*.json' -E '146\.56\.196\.204|106\.53\.107\.122|43\.132\.204\.177' /data/moox/prod /data/moox/storage /home/ubuntu/moox/prod 2>/dev/null | grep -viE 'password|secret|token|private_key|certificate' | head -n 80`
	for _, host := range hosts {
		if exec == nil {
			continue
		}
		stdout, err := exec(ctx, host.Address, script)
		item := Probe{From: host.Name, To: "runtime-config", Status: "inspected"}
		if err != nil {
			item.Status = "inspect_failed"
			item.Detail = err.Error()
		} else {
			for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
				if strings.TrimSpace(line) != "" {
					item.ConfigHits = append(item.ConfigHits, line)
				}
			}
		}
		out = append(out, item)
	}
	return out
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
