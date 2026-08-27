package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// platformClient talks to the Raff platform's cluster-scoped CSI API
// (api-gateway /api/v1/k8s/csi/*). The token identifies — and confines —
// this cluster; the platform resolves account, project and nodes from it.
type platformClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newPlatformClient(baseURL, token string) *platformClient {
	return &platformClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 100 * time.Second},
	}
}

// platformVolume mirrors the API's volume JSON.
type platformVolume struct {
	VolumeID int    `json:"volume_id"`
	PVName   string `json:"pv_name"`
	SizeGB   int    `json:"size_gb"`
	Status   string `json:"status"` // creating | available | attached
}

type platformAttach struct {
	DiskID int    `json:"disk_id"`
	Target string `json:"target"` // e.g. "vdb" → /dev/vdb on the node
}

type platformError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (c *platformClient) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1/k8s/csi"+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-CSI-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("platform request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("platform response read failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var pe platformError
		_ = json.Unmarshal(data, &pe)
		msg := pe.Error
		if msg == "" {
			msg = pe.Message
		}
		if msg == "" {
			msg = string(data)
		}
		return fmt.Errorf("platform %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("platform response parse failed: %w", err)
		}
	}
	return nil
}

// CreateVolume creates (or returns, idempotently) the volume for a PV name.
// pvcNamespace/pvcName are optional display metadata for the billing name.
func (c *platformClient) CreateVolume(ctx context.Context, pvName string, sizeGB int, pvcNamespace, pvcName string) (*platformVolume, error) {
	var v platformVolume
	err := c.do(ctx, http.MethodPost, "/volumes", map[string]interface{}{
		"pv_name":       pvName,
		"size_gb":       sizeGB,
		"pvc_namespace": pvcNamespace,
		"pvc_name":      pvcName,
	}, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetVolume returns one volume's state.
func (c *platformClient) GetVolume(ctx context.Context, volumeID int) (*platformVolume, error) {
	var v platformVolume
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/volumes/%d", volumeID), nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// DeleteVolume deletes a volume (idempotent on the platform side).
func (c *platformClient) DeleteVolume(ctx context.Context, volumeID int) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/volumes/%d", volumeID), nil, nil)
}

// AttachVolume attaches a volume to the node with this InternalIP and
// returns where the disk landed.
func (c *platformClient) AttachVolume(ctx context.Context, volumeID int, nodeIP string) (*platformAttach, error) {
	var a platformAttach
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/volumes/%d/attach", volumeID), map[string]interface{}{
		"node_ip": nodeIP,
	}, &a)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ExpandVolume grows a volume in place (grow-only; requires attachment —
// the platform rejects detached volumes and the resizer retries).
func (c *platformClient) ExpandVolume(ctx context.Context, volumeID, newSizeGB int) (*platformVolume, error) {
	var v platformVolume
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/volumes/%d/expand", volumeID), map[string]interface{}{
		"new_size_gb": newSizeGB,
	}, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// DetachVolume detaches a volume from its node (idempotent).
func (c *platformClient) DetachVolume(ctx context.Context, volumeID int) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/volumes/%d/detach", volumeID), nil, nil)
}
