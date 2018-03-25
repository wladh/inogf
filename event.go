package main

import (
	"context"
	"errors"
	"path"
	"regexp"

	"github.com/aristanetworks/glog"
	"github.com/aristanetworks/goarista/gnmi"
	pb "github.com/openconfig/gnmi/proto/gnmi"
)

// The types of events we deal with.
const (
	unknownEvent     = 0
	adminStatusEvent = 1
	prefixEvent      = 2
	prefixLenEvent   = 3
)

// This structure encapsulates the event type and data associated with it.
type event struct {
	evType int
	iface  string
}

// Regular expressions to match the path for each of the leaves we're interested in and to extract
// the interface name.
// This is not a good way to do things. In a real system, you'd probably want to parse and validate
// these updates (using probably something like goyang).
var eventsRe = map[int]*regexp.Regexp{
	adminStatusEvent: regexp.MustCompile("/interfaces/interface\\[name=(Ethernet[^]]*)\\]/state/admin-status"),
	prefixEvent:      regexp.MustCompile("/interfaces/interface\\[name=(Ethernet[^]]*)\\]/.*/address\\[ip=[^]]*\\]/state/ip"),
	prefixLenEvent:   regexp.MustCompile("/interfaces/interface\\[name=(Ethernet[^]]*)\\]/.*/address\\[ip=[^]]*\\]/state/prefix-length"),
}

// Parses the gNMI notification paths against the regexp map above and returns the
// corresponding event.
func getEvent(path string) *event {
	for evType, re := range eventsRe {
		if groups := re.FindStringSubmatch(path); groups != nil {
			return &event{
				evType: evType,
				iface:  groups[1],
			}
		}
	}

	return &event{evType: unknownEvent}
}

// Finds the corresponding state machine object for the interface and dispatches the event to
// the right handler.
func dispatchEvent(ctx context.Context, client pb.GNMIClient, ev *event, value string) {
	switch ev.evType {
	case adminStatusEvent:
		getInterfaceSm(ctx, client, ev.iface).adminStatusEventHandler(ev, value)
	case prefixEvent:
		getInterfaceSm(ctx, client, ev.iface).prefixEventHandler(ev, value)
	case prefixLenEvent:
		getInterfaceSm(ctx, client, ev.iface).prefixLenEventHandler(ev, value)
	default:
		glog.Errorf("Unknown event: %#v", ev)
	}
}

// The main event loop receives messages from gNMI, validates them and then dispatches them.
func eventLoop(ctx context.Context, client pb.GNMIClient,
	respChan chan *pb.SubscribeResponse, errChan chan error) error {
	// Loop forever
	for {
		// Read either from responses or error channel (whichever is ready first)
		select {
		case resp := <-respChan:
			// Check the type of response.
			switch resp := resp.Response.(type) {
			case *pb.SubscribeResponse_Error:
				return errors.New(resp.Error.Message)
			case *pb.SubscribeResponse_SyncResponse:
				// This message indicates that the initial state for the subscribed paths has been
				// completely streamed out. We don't need to differentiate between initial state
				// and on-going updates in our program.
				if !resp.SyncResponse {
					return errors.New("initial sync failed")
				}
			case *pb.SubscribeResponse_Update:
				// This is the common path prefix for all updates in this message.
				// We use StrPath function to transform the path components into a slash delimited
				// string. This is fine for our examples, but in a real system you probably want to
				// parse and validate these paths (with something like goyang).
				prefix := gnmi.StrPath(resp.Update.Prefix)
				// We're looping over Updates. In this example, we don't look at the Delete field
				// of the response (which would indicate that the IP address was deconfigured, for
				// instance).
				for _, update := range resp.Update.Update {
					// Build the absolute path
					path := path.Join(prefix, gnmi.StrPath(update.Path))
					// Get the value at this path as a string.
					// Values can be encoded in different ways in gNMI but StrUpdateVale takes care
					// of that for us.
					value := gnmi.StrUpdateVal(update)
					glog.V(5).Infof("Received update for path %s value %s", path, value)

					event := getEvent(path)

					glog.V(5).Infof("Dispatching event %#v", event)
					dispatchEvent(ctx, client, event, value)
				}
			}
		case err := <-errChan:
			return err
		}
	}
}
