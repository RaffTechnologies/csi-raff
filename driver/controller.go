package driver

import (
	"context"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	gib = int64(1024 * 1024 * 1024)

	// minSizeGB is the Volumes product floor — smaller PVC requests are
	// rounded up (the customer gets and pays for 10 GB).
	minSizeGB = 10
	// maxSizeGB is the Volumes product ceiling.
	maxSizeGB = 10000

	// createPollInterval/Timeout bound the wait for the async platform
	// create. On timeout we return an error; external-provisioner retries
	// and the platform create is idempotent on the PV name.
	createPollInterval = 3 * time.Second
	createPollTimeout  = 90 * time.Second
)

// ControllerGetCapabilities implements csi.ControllerServer.
func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	caps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	}
	out := make([]*csi.ControllerServiceCapability, 0, len(caps))
	for _, c := range caps {
		out = append(out, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{Type: c},
			},
		})
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: out}, nil
}

// validateCapabilities rejects anything but single-node mount volumes —
// block attach means one node at a time (RWO), like EBS/DO.
func validateCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return status.Error(codes.InvalidArgument, "volume capabilities are required")
	}
	for _, c := range caps {
		if c.GetBlock() != nil {
			return status.Error(codes.InvalidArgument, "raw block volumes are not supported")
		}
		switch c.GetAccessMode().GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
		default:
			return status.Error(codes.InvalidArgument, "only ReadWriteOnce access is supported")
		}
	}
	return nil
}

// requestedSizeGB converts a CSI capacity range to whole GB within product
// bounds. Requests below the floor round UP to it (a 1Gi PVC provisions and
// bills 10 GB); a limit below the floor is unsatisfiable.
func requestedSizeGB(cr *csi.CapacityRange) (int, error) {
	required := int64(minSizeGB) * gib
	if cr.GetRequiredBytes() > 0 {
		required = cr.GetRequiredBytes()
	}
	sizeGB := int((required + gib - 1) / gib) // ceil to GB
	if sizeGB < minSizeGB {
		sizeGB = minSizeGB
	}
	if sizeGB > maxSizeGB {
		return 0, status.Errorf(codes.OutOfRange, "requested size exceeds the %d GB maximum", maxSizeGB)
	}
	if limit := cr.GetLimitBytes(); limit > 0 && int64(sizeGB)*gib > limit {
		return 0, status.Errorf(codes.OutOfRange, "limit below the %d GB minimum volume size", minSizeGB)
	}
	return sizeGB, nil
}

// CreateVolume implements csi.ControllerServer. Idempotent on req.Name (the
// PV name external-provisioner generates).
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	if err := validateCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, err
	}
	sizeGB, err := requestedSizeGB(req.GetCapacityRange())
	if err != nil {
		return nil, err
	}

	// With --extra-create-metadata the provisioner passes the claim's
	// identity — the platform uses it for a human-readable billing name
	// (e.g. "k7n92s-data-postgres" instead of the pvc-<uuid> handle).
	pvcName := req.GetParameters()["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := req.GetParameters()["csi.storage.k8s.io/pvc/namespace"]

	vol, err := d.platform.CreateVolume(ctx, req.GetName(), sizeGB, pvcNamespace, pvcName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	// The platform create is async — wait until the backing image exists so
	// the PV only binds to real storage.
	deadline := time.Now().Add(createPollTimeout)
	for vol.Status == "creating" {
		if time.Now().After(deadline) {
			return nil, status.Errorf(codes.Unavailable, "volume %d is still provisioning", vol.VolumeID)
		}
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-time.After(createPollInterval):
		}
		vol, err = d.platform.GetVolume(ctx, vol.VolumeID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      strconv.Itoa(vol.VolumeID),
			CapacityBytes: int64(vol.SizeGB) * gib,
		},
	}, nil
}

// DeleteVolume implements csi.ControllerServer (idempotent).
func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID, err := strconv.Atoi(req.GetVolumeId())
	if err != nil {
		// An unparseable id can't be one of ours — deleting it is a no-op.
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err := d.platform.DeleteVolume(ctx, volumeID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume implements csi.ControllerServer: attach the volume
// to the node. NodeId is the node's InternalIP (see NodeGetInfo); the
// platform maps it to the worker VM and returns the device target, which
// travels to NodeStageVolume via PublishContext.
func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	volumeID, err := strconv.Atoi(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown volume id")
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}

	attach, err := d.platform.AttachVolume(ctx, volumeID, req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			"target": attach.Target, // NodeStageVolume mounts /dev/<target>
		},
	}, nil
}

// ControllerUnpublishVolume implements csi.ControllerServer (idempotent).
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	volumeID, err := strconv.Atoi(req.GetVolumeId())
	if err != nil {
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}
	if err := d.platform.DetachVolume(ctx, volumeID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities implements csi.ControllerServer.
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	volumeID, err := strconv.Atoi(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.NotFound, "unknown volume id")
	}
	if _, err := d.platform.GetVolume(ctx, volumeID); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := validateCapabilities(req.GetVolumeCapabilities()); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{}, nil // supported=false
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// --- unimplemented controller RPCs (capabilities above exclude them) ---

func (d *Driver) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "resize lands in a later phase")
}

func (d *Driver) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
