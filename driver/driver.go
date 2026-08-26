package driver

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
)

// DriverName is the CSI driver name registered with Kubernetes.
const DriverName = "csi.raff.technologies"

// DriverVersion is stamped by the release workflow via -ldflags.
var DriverVersion = "dev"

// Config carries everything either mode needs.
type Config struct {
	Mode     string // "controller" | "node"
	Endpoint string // unix:///csi/csi.sock
	APIURL   string // controller: platform base URL
	Token    string // controller: cluster-scoped raff_csi_ token
	NodeIP   string // node: this node's InternalIP (status.hostIP)
}

// Driver is the CSI server for one mode.
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer

	cfg      Config
	platform *platformClient // controller mode only
}

// New validates the config for the chosen mode.
func New(cfg Config) (*Driver, error) {
	d := &Driver{cfg: cfg}
	switch cfg.Mode {
	case "controller":
		if cfg.APIURL == "" || cfg.Token == "" {
			return nil, fmt.Errorf("controller mode requires RAFF_CSI_API_URL and RAFF_CSI_TOKEN")
		}
		d.platform = newPlatformClient(cfg.APIURL, cfg.Token)
	case "node":
		if cfg.NodeIP == "" {
			return nil, fmt.Errorf("node mode requires RAFF_NODE_IP (downward API status.hostIP)")
		}
	default:
		return nil, fmt.Errorf("unknown mode %q (want controller or node)", cfg.Mode)
	}
	return d, nil
}

// Run serves CSI gRPC on the unix socket until the process is stopped.
func (d *Driver) Run() error {
	addr := strings.TrimPrefix(d.cfg.Endpoint, "unix://")
	// A previous run's socket blocks the bind — always start fresh.
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	srv := grpc.NewServer(grpc.UnaryInterceptor(logInterceptor))
	csi.RegisterIdentityServer(srv, d)
	switch d.cfg.Mode {
	case "controller":
		csi.RegisterControllerServer(srv, d)
	case "node":
		csi.RegisterNodeServer(srv, d)
	}

	log.Printf("csi-raff %s starting: mode=%s endpoint=%s", DriverVersion, d.cfg.Mode, d.cfg.Endpoint)
	return srv.Serve(lis)
}
