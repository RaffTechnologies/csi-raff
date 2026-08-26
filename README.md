# csi-raff

CSI driver for Raff Technologies managed Kubernetes: PersistentVolumes are
backed by the [Raff Volumes product](https://docs.rafftechnologies.com) —
Ceph-backed block devices the platform attaches to worker VMs.

One binary, two modes:

- **controller** (Deployment): CreateVolume / DeleteVolume /
  ControllerPublish / ControllerUnpublish against the platform's
  cluster-scoped `/k8s/csi/*` API, authenticated by a `raff_csi_` token the
  platform mints per cluster.
- **node** (DaemonSet): formats (first use) and mounts the attached device —
  `/dev/<target>` from PublishContext. Purely local, needs no credentials.

`raff-block` is the default StorageClass in every new managed cluster.
ReadWriteOnce only; 10–10000 GB; billed hourly per GB as regular Volumes.

The platform deploys this automatically ([deploy/csi-raff.yaml](deploy/csi-raff.yaml)
with the image tag pinned per release) — customers never install it by hand.

## Development

```bash
go test ./...
go build .
```

Releases: push a `v*` tag — GitHub Actions builds and pushes
`ghcr.io/rafftechnologies/csi-raff`.
