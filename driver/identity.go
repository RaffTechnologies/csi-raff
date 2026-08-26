package driver

import (
	"context"
	"log"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
)

// logInterceptor logs every CSI call with its outcome — kubelet and the
// sidecars retry on error, so one line per call keeps the story readable.
func logInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	resp, err := handler(ctx, req)
	method := info.FullMethod[strings.LastIndex(info.FullMethod, "/")+1:]
	if err != nil {
		log.Printf("%s error: %v", method, err)
	} else if method != "Probe" && method != "NodeGetCapabilities" && method != "ControllerGetCapabilities" {
		log.Printf("%s ok", method)
	}
	return resp, err
}

// GetPluginInfo implements csi.IdentityServer.
func (d *Driver) GetPluginInfo(ctx context.Context, req *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          DriverName,
		VendorVersion: DriverVersion,
	}, nil
}

// GetPluginCapabilities implements csi.IdentityServer.
func (d *Driver) GetPluginCapabilities(ctx context.Context, req *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
		},
	}, nil
}

// Probe implements csi.IdentityServer.
func (d *Driver) Probe(ctx context.Context, req *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}
