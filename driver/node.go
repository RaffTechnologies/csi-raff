package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
)

const (
	// devWaitTimeout bounds the wait for the hotplugged device node to
	// appear after the platform reports the attach.
	devWaitTimeout  = 60 * time.Second
	devWaitInterval = time.Second

	defaultFSType = "ext4"

	// maxVolumesPerNode caps PVCs per worker: ~20 virtio targets minus the
	// OS/context disks, with headroom.
	maxVolumesPerNode = 16
)

// NodeGetInfo implements csi.NodeServer. The node id is this node's
// InternalIP — the identity the platform resolves to a worker VM.
func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:            d.cfg.NodeIP,
		MaxVolumesPerNode: maxVolumesPerNode,
	}, nil
}

// NodeGetCapabilities implements csi.NodeServer.
func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

// NodeStageVolume implements csi.NodeServer: wait for the attached device,
// format on first use, and mount it at the staging path.
func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	target := req.GetPublishContext()["target"]
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "publish context is missing the device target")
	}
	staging := req.GetStagingTargetPath()
	if staging == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	device := "/dev/" + filepath.Base(target) // never let a path escape /dev
	if err := waitForDevice(ctx, device); err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	fsType := defaultFSType
	if m := req.GetVolumeCapability().GetMount(); m != nil && m.GetFsType() != "" {
		fsType = m.GetFsType()
	}

	mounter := mount.SafeFormatAndMount{Interface: mount.New(""), Exec: utilexec.New()}
	mounted, err := mounter.IsMountPoint(staging)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "failed to inspect staging path: %v", err)
		}
		if err := os.MkdirAll(staging, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create staging path: %v", err)
		}
	}
	if mounted {
		return &csi.NodeStageVolumeResponse{}, nil // idempotent
	}

	// FormatAndMount formats only when the device carries no filesystem —
	// re-attached volumes keep their data.
	if err := mounter.FormatAndMount(device, staging, fsType, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to format/mount %s: %v", device, err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume implements csi.NodeServer (idempotent).
func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	if staging == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if err := mount.CleanupMountPoint(staging, mount.New(""), true); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount staging path: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume implements csi.NodeServer: bind-mount staging → pod path.
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()
	if staging == "" || targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging and target paths are required")
	}

	mounter := mount.New("")
	mounted, err := mounter.IsMountPoint(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "failed to inspect target path: %v", err)
		}
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create target path: %v", err)
		}
	}
	if mounted {
		return &csi.NodePublishVolumeResponse{}, nil // idempotent
	}

	opts := []string{"bind"}
	if req.GetReadonly() {
		opts = append(opts, "ro")
	}
	if err := mounter.Mount(staging, targetPath, "", opts); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume implements csi.NodeServer (idempotent).
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if err := mount.CleanupMountPoint(targetPath, mount.New(""), true); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount target path: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// --- unimplemented node RPCs ---

func (d *Driver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "resize lands in a later phase")
}

// waitForDevice polls until the hotplugged block device node exists.
func waitForDevice(ctx context.Context, device string) error {
	deadline := time.Now().Add(devWaitTimeout)
	for {
		if _, err := os.Stat(device); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("device %s did not appear within %s", device, devWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(devWaitInterval):
		}
	}
}
