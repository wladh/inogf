package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/aristanetworks/glog"
	"github.com/aristanetworks/goarista/gnmi"
	pb "github.com/openconfig/gnmi/proto/gnmi"
)

const (
	// The paths we're subscribing to.
	// This path will have the value "UP" or "DOWN"
	adminStatusPath = "/interfaces/interface/state/admin-status"
	// This path will have 2 leaves (or subpaths), one for "ip" and one for "prefix-length"
	ipv4AddressPath = "/interfaces/interface/subinterfaces/subinterface/ipv4/addresses/address/state"

	// The path we need to update in order to configure the IP address for the interface.
	ipv4ConfigPath = "/interfaces/interface[name=%s]/subinterfaces/subinterface[index=0]/ipv4/addresses/address[ip=%s]"
	// The JSON object that is the new value at the above path.
	ipv4ConfigRequest = `{"config": {"ip": "%s", "prefix-length": %d}}`
)

// setPrefix will send a gNMI "udapte" request that will set the interface's IP and prefixLen.
func setPrefix(ctx context.Context, client pb.GNMIClient,
	iface string, prefix string, prefixLen int) error {
	op := &gnmi.Operation{
		Type: "update",
		Path: gnmi.SplitPath(fmt.Sprintf(ipv4ConfigPath, iface, prefix)),
		Val:  fmt.Sprintf(ipv4ConfigRequest, prefix, prefixLen),
	}
	return gnmi.Set(ctx, client, []*gnmi.Operation{op})
}

func main() {
	// Parse and validate command line arguments
	cfg := &gnmi.Config{}
	flag.StringVar(&cfg.Addr, "addr", "", "Address of gNMI gRPC server")
	flag.StringVar(&cfg.CAFile, "cafile", "", "Path to server TLS certificate file")
	flag.StringVar(&cfg.CertFile, "certfile", "", "Path to client TLS certificate file")
	flag.StringVar(&cfg.KeyFile, "keyfile", "", "Path to client TLS private key file")
	flag.StringVar(&cfg.Password, "password", "", "Password to authenticate with")
	flag.StringVar(&cfg.Username, "username", "", "Username to authenticate with")
	flag.BoolVar(&cfg.TLS, "tls", false, "Enable TLS")
	flag.Parse()
	if cfg.Addr == "" {
		glog.Fatal("error: address not specified")
	}

	// Init the IP database
	initIPs(200, 24)

	// Connect to the device via gNMI
	ctx := gnmi.NewContext(context.Background(), cfg)
	client, err := gnmi.Dial(cfg)
	if err != nil {
		glog.Fatal(err)
	}

	// Create 2 channels for receiving gNMI messages.
	respChan := make(chan *pb.SubscribeResponse)
	errChan := make(chan error)
	// Close the channels when returning from this function.
	defer close(respChan)
	defer close(errChan)
	// Subscribe to admin-status and ipv4 addresses updates.
	// This will run in a different thread (or goroutine, as it's called in go) and will pass the
	// messages to the respChan and errors on the errChan.
	go gnmi.Subscribe(ctx, client,
		gnmi.SplitPaths([]string{adminStatusPath, ipv4AddressPath}), respChan, errChan)

	// Run the main event loop.
	if err = eventLoop(ctx, client, respChan, errChan); err != nil {
		glog.Fatal(err)
	}
}
