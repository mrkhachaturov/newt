package docker

import (
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

func TestShortServiceID(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"", ""},
		{"abc", "abc"},
		{"123456789012", "123456789012"},
		{"1234567890123", "123456789012"},
		{"abcdef1234567890abcdef1234", "abcdef123456"},
	}
	for _, c := range cases {
		if got := shortServiceID(c.in); got != c.out {
			t.Errorf("shortServiceID(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestServiceState(t *testing.T) {
	cases := []struct {
		name string
		svc  swarm.Service
		want string
	}{
		{
			name: "nil status",
			svc:  swarm.Service{},
			want: "exited",
		},
		{
			name: "zero running tasks",
			svc:  swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 0, DesiredTasks: 1}},
			want: "exited",
		},
		{
			name: "one running task",
			svc:  swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 1, DesiredTasks: 1}},
			want: "running",
		},
		{
			name: "partial replicas running",
			svc:  swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 2, DesiredTasks: 3}},
			want: "running",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceState(c.svc); got != c.want {
				t.Errorf("serviceState = %q, want %q", got, c.want)
			}
		})
	}
}

func TestServiceReplicaStatus(t *testing.T) {
	cases := []struct {
		name string
		svc  swarm.Service
		want string
	}{
		{"nil status", swarm.Service{}, ""},
		{"healthy", swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 1, DesiredTasks: 1}}, "1/1"},
		{"degraded", swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 2, DesiredTasks: 3}}, "2/3"},
		{"all stopped", swarm.Service{ServiceStatus: &swarm.ServiceStatus{RunningTasks: 0, DesiredTasks: 3}}, "0/3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceReplicaStatus(c.svc); got != c.want {
				t.Errorf("serviceReplicaStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestServiceEndpointPorts(t *testing.T) {
	in := []swarm.PortConfig{
		{TargetPort: 80, PublishedPort: 8080, Protocol: network.TCP},
		{TargetPort: 53, PublishedPort: 0, Protocol: network.UDP},
	}
	got := serviceEndpointPorts(in)
	if len(got) != 2 {
		t.Fatalf("got %d ports, want 2", len(got))
	}
	if got[0].PrivatePort != 80 || got[0].PublicPort != 8080 || got[0].Type != "tcp" {
		t.Errorf("port[0] = %+v", got[0])
	}
	if got[1].PrivatePort != 53 || got[1].PublicPort != 0 || got[1].Type != "udp" {
		t.Errorf("port[1] = %+v", got[1])
	}

	if got := serviceEndpointPorts(nil); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
}

func TestServiceNetworks(t *testing.T) {
	svc := swarm.Service{
		Endpoint: swarm.Endpoint{
			VirtualIPs: []swarm.EndpointVirtualIP{
				{NetworkID: "net-a", Addr: netip.MustParsePrefix("10.0.1.5/24")},
				{NetworkID: "net-b", Addr: netip.MustParsePrefix("10.0.2.5/24")},
			},
		},
	}
	got := serviceNetworks(svc)
	if len(got) != 2 {
		t.Fatalf("got %d networks, want 2", len(got))
	}
	if got["net-a"].IPAddress != "10.0.1.5/24" || got["net-a"].NetworkID != "net-a" {
		t.Errorf("net-a = %+v", got["net-a"])
	}
	if got["net-b"].IPAddress != "10.0.2.5/24" {
		t.Errorf("net-b = %+v", got["net-b"])
	}

	if got := serviceNetworks(swarm.Service{}); got != nil {
		t.Errorf("no VirtualIPs should return nil, got %v", got)
	}
}

func TestServiceAttachedToAny(t *testing.T) {
	allowed := map[string]struct{}{"net-allowed": {}}

	cases := []struct {
		name string
		svc  swarm.Service
		want bool
	}{
		{
			name: "no networks",
			svc:  swarm.Service{},
			want: false,
		},
		{
			name: "vip on allowed network",
			svc: swarm.Service{Endpoint: swarm.Endpoint{
				VirtualIPs: []swarm.EndpointVirtualIP{{NetworkID: "net-allowed"}},
			}},
			want: true,
		},
		{
			name: "vip on other network only",
			svc: swarm.Service{Endpoint: swarm.Endpoint{
				VirtualIPs: []swarm.EndpointVirtualIP{{NetworkID: "net-other"}},
			}},
			want: false,
		},
		{
			name: "task template networks include allowed",
			svc: swarm.Service{Spec: swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{
				Networks: []swarm.NetworkAttachmentConfig{{Target: "net-allowed"}},
			}}},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceAttachedToAny(c.svc, allowed); got != c.want {
				t.Errorf("serviceAttachedToAny = %v, want %v", got, c.want)
			}
		})
	}
}

func TestServiceToContainer(t *testing.T) {
	created := time.Date(2026, 5, 24, 19, 0, 0, 0, time.UTC)
	svc := swarm.Service{
		ID: "abcdef1234567890",
		Meta: swarm.Meta{
			CreatedAt: created,
		},
		Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{
				Name: "stack_app",
				Labels: map[string]string{
					"pangolin.public-resources.app.name": "app",
				},
			},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: "myorg/myapp:1.0"},
			},
		},
		Endpoint: swarm.Endpoint{
			Ports: []swarm.PortConfig{
				{TargetPort: 80, PublishedPort: 8080, Protocol: network.TCP},
			},
			VirtualIPs: []swarm.EndpointVirtualIP{
				{NetworkID: "net-1", Addr: netip.MustParsePrefix("10.0.0.5/24")},
			},
		},
		ServiceStatus: &swarm.ServiceStatus{RunningTasks: 1, DesiredTasks: 1},
	}

	got := serviceToContainer(svc)

	if got.ID != "abcdef123456" {
		t.Errorf("ID = %q, want short %q", got.ID, "abcdef123456")
	}
	if got.Name != "stack_app" || got.Hostname != "stack_app" {
		t.Errorf("Name/Hostname = %q/%q, want stack_app", got.Name, got.Hostname)
	}
	if got.Image != "myorg/myapp:1.0" {
		t.Errorf("Image = %q", got.Image)
	}
	if got.State != "running" {
		t.Errorf("State = %q, want running", got.State)
	}
	if got.Status != "1/1" {
		t.Errorf("Status = %q, want 1/1", got.Status)
	}
	if got.Labels["pangolin.public-resources.app.name"] != "app" {
		t.Errorf("Labels missing pangolin entry: %+v", got.Labels)
	}
	if got.Created != created.Unix() {
		t.Errorf("Created = %d, want %d", got.Created, created.Unix())
	}
	if len(got.Ports) != 1 || got.Ports[0].PrivatePort != 80 {
		t.Errorf("Ports = %+v", got.Ports)
	}
	if got.Networks["net-1"].IPAddress != "10.0.0.5/24" {
		t.Errorf("Networks = %+v", got.Networks)
	}
}

func TestServiceToContainerFallsBackToEndpointSpecPorts(t *testing.T) {
	svc := swarm.Service{
		Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "fresh_app"},
			EndpointSpec: &swarm.EndpointSpec{
				Ports: []swarm.PortConfig{
					{TargetPort: 5984, PublishedPort: 0, Protocol: network.TCP},
				},
			},
		},
	}
	got := serviceToContainer(svc)
	if len(got.Ports) != 1 || got.Ports[0].PrivatePort != 5984 {
		t.Errorf("expected fallback to EndpointSpec.Ports, got %+v", got.Ports)
	}
}

func TestShortEventActorID(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"", ""},
		{"short", "short"},
		{"123456789012", "123456789012"},
		{"1234567890123abcdef", "123456789012"},
	}
	for _, c := range cases {
		if got := shortEventActorID(c.in); got != c.out {
			t.Errorf("shortEventActorID(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}
