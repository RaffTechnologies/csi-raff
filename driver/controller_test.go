package driver

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

func TestRequestedSizeGB(t *testing.T) {
	cases := []struct {
		name     string
		required int64
		limit    int64
		want     int
		wantErr  bool
	}{
		{"empty range gets the floor", 0, 0, 10, false},
		{"1Gi rounds up to the floor", 1 * gib, 0, 10, false},
		{"exact 20Gi", 20 * gib, 0, 20, false},
		{"20Gi+1 rounds up", 20*gib + 1, 0, 21, false},
		{"limit below floor unsatisfiable", 1 * gib, 5 * gib, 0, true},
		{"above product max", 10001 * gib, 0, 0, true},
	}
	for _, c := range cases {
		got, err := requestedSizeGB(&csi.CapacityRange{RequiredBytes: c.required, LimitBytes: c.limit})
		if c.wantErr != (err != nil) {
			t.Errorf("%s: err = %v, wantErr %t", c.name, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("%s: size = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestValidateCapabilities(t *testing.T) {
	mount := func(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
		return &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		}
	}
	if err := validateCapabilities([]*csi.VolumeCapability{mount(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)}); err != nil {
		t.Errorf("RWO must be accepted: %v", err)
	}
	if err := validateCapabilities([]*csi.VolumeCapability{mount(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)}); err == nil {
		t.Error("RWX must be rejected")
	}
	block := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
	if err := validateCapabilities([]*csi.VolumeCapability{block}); err == nil {
		t.Error("raw block must be rejected")
	}
	if err := validateCapabilities(nil); err == nil {
		t.Error("empty capabilities must be rejected")
	}
}
