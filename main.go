// csi-raff is the Raff Technologies CSI driver: PersistentVolumes in managed
// Kubernetes clusters are backed by the Raff Volumes product (Ceph-backed
// block devices attached to worker VMs by the platform).
//
// One binary, two modes:
//   - controller: CSI Controller + Identity. Talks to the Raff platform's
//     cluster-scoped /k8s/csi/* API (create/delete/attach/detach).
//   - node: CSI Node + Identity. Purely local — formats and mounts the
//     device the platform attached (/dev/<target> from PublishContext).
package main

import (
	"flag"
	"log"
	"os"

	"github.com/rafftechnologies/csi-raff/driver"
)

func main() {
	var (
		mode     = flag.String("mode", "", "controller | node")
		endpoint = flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
	)
	flag.Parse()

	cfg := driver.Config{
		Mode:     *mode,
		Endpoint: *endpoint,
		// Controller only:
		APIURL: os.Getenv("RAFF_CSI_API_URL"), // e.g. https://api.rafftechnologies.com
		Token:  os.Getenv("RAFF_CSI_TOKEN"),   // cluster-scoped raff_csi_ token
		// Node only:
		NodeIP: os.Getenv("RAFF_NODE_IP"), // downward API status.hostIP — the platform's node identity
	}

	d, err := driver.New(cfg)
	if err != nil {
		log.Fatalf("csi-raff: %v", err)
	}
	if err := d.Run(); err != nil {
		log.Fatalf("csi-raff: %v", err)
	}
}
