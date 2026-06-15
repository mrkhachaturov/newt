package docker

import (
	"context"
	"fmt"
	"strconv"

	"github.com/fosrl/newt/logger"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// isSwarmManager returns true when the connected Docker daemon is an active
// swarm manager node. Only managers can list cluster-wide services.
func isSwarmManager(ctx context.Context, cli *client.Client) bool {
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		logger.Debug("Docker Info call failed, assuming non-swarm: %v", err)
		return false
	}
	return info.Info.Swarm.LocalNodeState == swarm.LocalNodeStateActive && info.Info.Swarm.ControlAvailable
}

// listSwarmServiceContainers enumerates Swarm services visible to the
// connected manager and converts each into a synthetic Container entry.
// ContainerList alone only returns containers on the local daemon, so on a
// multi-node swarm a single Newt instance would otherwise see ~1/Nth of the
// cluster. ServiceList is cluster-wide on any manager.
//
// When allowedNetworkIDs is non-empty, only services attached to one of those
// networks are returned. This mirrors the same-network filter ContainerList
// applies under DOCKER_ENFORCE_NETWORK_VALIDATION; nil or empty means no
// filter (consistent with the standalone-Docker behavior).
func listSwarmServiceContainers(
	ctx context.Context,
	cli *client.Client,
	allowedNetworkIDs map[string]struct{},
) ([]Container, error) {
	// Status:true asks the daemon to populate Service.ServiceStatus with
	// running/desired/completed task counts. Without it the field is nil and
	// serviceState cannot tell a healthy service from a stopped one.
	result, err := cli.ServiceList(ctx, client.ServiceListOptions{Status: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list swarm services: %w", err)
	}

	entries := make([]Container, 0, len(result.Items))
	for _, svc := range result.Items {
		if len(allowedNetworkIDs) > 0 && !serviceAttachedToAny(svc, allowedNetworkIDs) {
			continue
		}
		entries = append(entries, serviceToContainer(svc))
	}
	return entries, nil
}

// serviceToContainer maps a Swarm Service into a Container entry. Labels come
// from Service.Spec.Labels (the `deploy.labels` block in compose). State is
// derived from the live task count so downstream consumers that filter on
// State == "running" behave consistently with how they handle real containers.
func serviceToContainer(svc swarm.Service) Container {
	name := svc.Spec.Annotations.Name

	var image string
	if svc.Spec.TaskTemplate.ContainerSpec != nil {
		image = svc.Spec.TaskTemplate.ContainerSpec.Image
	}

	ports := serviceEndpointPorts(svc.Endpoint.Ports)
	if len(ports) == 0 && svc.Spec.EndpointSpec != nil {
		// Endpoint.Ports may be empty on just-created services; fall back to
		// the declared spec so labels-only resources still get port info.
		ports = serviceEndpointPorts(svc.Spec.EndpointSpec.Ports)
	}

	return Container{
		ID:       shortServiceID(svc.ID),
		Name:     name,
		Image:    image,
		State:    serviceState(svc),
		Status:   serviceReplicaStatus(svc),
		Ports:    ports,
		Labels:   svc.Spec.Labels,
		Created:  svc.CreatedAt.Unix(),
		Networks: serviceNetworks(svc),
		Hostname: name, // overlay DNS resolves the service name cluster-wide
	}
}

// serviceState reports "running" when at least one task of the service is
// running, otherwise "exited". Mirrors how real container State is used by
// downstream consumers (e.g. blueprint discovery filters on State=="running").
func serviceState(svc swarm.Service) string {
	if svc.ServiceStatus != nil && svc.ServiceStatus.RunningTasks > 0 {
		return "running"
	}
	return "exited"
}

func serviceReplicaStatus(svc swarm.Service) string {
	if svc.ServiceStatus == nil {
		return ""
	}
	return strconv.FormatUint(svc.ServiceStatus.RunningTasks, 10) + "/" +
		strconv.FormatUint(svc.ServiceStatus.DesiredTasks, 10)
}

func serviceEndpointPorts(in []swarm.PortConfig) []Port {
	if len(in) == 0 {
		return nil
	}
	out := make([]Port, 0, len(in))
	for _, p := range in {
		out = append(out, Port{
			PrivatePort: int(p.TargetPort),
			PublicPort:  int(p.PublishedPort),
			Type:        string(p.Protocol),
		})
	}
	return out
}

// serviceNetworks builds a Networks map keyed by network ID. We don't resolve
// overlay network names from the service object (would require a separate
// NetworkInspect per attachment); the existing Container.Networks contract
// only requires the key to be non-empty and the value to carry routing info.
// VirtualIPs carry the per-network VIP address overlay traffic targets.
func serviceNetworks(svc swarm.Service) map[string]Network {
	if len(svc.Endpoint.VirtualIPs) == 0 {
		return nil
	}
	out := make(map[string]Network, len(svc.Endpoint.VirtualIPs))
	for _, vip := range svc.Endpoint.VirtualIPs {
		out[vip.NetworkID] = Network{
			NetworkID: vip.NetworkID,
			IPAddress: vip.Addr.String(),
		}
	}
	return out
}

// serviceAttachedToAny reports whether the service is attached to at least
// one of the network IDs in allowed. Checks the two places network refs can
// appear: runtime VirtualIPs (set once VIP allocation lands) and the
// TaskTemplate's Networks block (current spec). The deprecated top-level
// ServiceSpec.Networks block was removed in the moby/moby client API.
func serviceAttachedToAny(svc swarm.Service, allowed map[string]struct{}) bool {
	for _, vip := range svc.Endpoint.VirtualIPs {
		if _, ok := allowed[vip.NetworkID]; ok {
			return true
		}
	}
	for _, n := range svc.Spec.TaskTemplate.Networks {
		if _, ok := allowed[n.Target]; ok {
			return true
		}
	}
	return false
}

func shortServiceID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
