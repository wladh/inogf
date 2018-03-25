package main

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aristanetworks/glog"

	pb "github.com/openconfig/gnmi/proto/gnmi"
)

// A global map from interface name to its state machine object.
var interfaces = make(map[string]*interfaceSm)

// State machine states.
const (
	unknown    = 0
	adminDown  = 1
	adminUp    = 2
	configured = 3
)

func stateName(state int) string {
	switch state {
	case unknown:
		return "unknown"
	case adminDown:
		return "adminDown"
	case adminUp:
		return "adminUp"
	case configured:
		return "configured"
	default:
		return "invalid"
	}
}

// Interface state machine.
// Each interface will have its own instance of the state machine.
type interfaceSm struct {
	state     int
	name      string
	prefix    string
	prefixLen int

	// Timer for waiting on receiving the current configuration from interface.
	// Since the messages from gNMI arrive asynchronously we can't know in advance if we got all
	// the information we needed, so we wait for a while.
	timer *time.Timer

	// A lock to make event handling thread-safe. While the event loop is single threaded,
	// the timer will be fired from another thread.
	// A better design would be to have the timer generate an event into the event loop,
	// but I wanted to keep the code simple and not use additional channels.
	mu sync.Mutex

	// Client and context needed to communicate with gNMI server.
	// In a better design these would be part of a state machines manager.
	ctx    context.Context
	client pb.GNMIClient
}

// Retrieves or creates the state machine for the given interface.
func getInterfaceSm(ctx context.Context, client pb.GNMIClient, iface string) *interfaceSm {
	sm, ok := interfaces[iface]
	if !ok {
		sm = &interfaceSm{
			name:   iface,
			ctx:    ctx,
			client: client,
		}
		interfaces[iface] = sm
	}

	return sm
}

// This function returns true if the state machine has all the information needed to configure the
// interface.
func (sm *interfaceSm) configComplete() bool {
	return sm.prefix != "" && sm.prefixLen > 0
}

// Transition the state machine to "adminDown" state. We cancel the configuration receive timer and
// release its IP.
func (sm *interfaceSm) adminDown() {
	sm.state = adminDown

	if sm.timer != nil {
		sm.timer.Stop()
	}
	ipDB.releaseIP(sm.prefix)
}

// Transition the state machine to "adminUp" state. If we didn't receive the complete interface
// configuration, start a timer to wait for it.
func (sm *interfaceSm) adminUp() {
	sm.state = adminUp

	// If we don't have the configuration yet, start a 20 seconds timer.
	if !sm.configComplete() {
		sm.timer = time.AfterFunc(20*time.Second, sm.timerEventHandler)
	}
}

// Transition the state machine to "configured" state. If we received configuration for this
// interface, try to reconcile it with the database, otherwise get a new IP.
// If the interface's IP address needs to be changed, make the request to gNMI server.
func (sm *interfaceSm) configured() {
	sm.state = configured

	if sm.configComplete() {
		// If we already have the interface's configuration, try to reconcile it.
		var ok bool
		if sm.prefix, sm.prefixLen, ok = ipDB.reconcile(sm.name, sm.prefix, sm.prefixLen); ok {
			// If it was successfully reconciled, there's nothing more to do, as we keep the
			// current configuration.
			glog.V(5).Infof("Prefix %s/%d reconciled for %s", sm.prefix, sm.prefixLen, sm.name)
			return
		}
	} else {
		// If the interface doesn't have any IP address configured, assign it a new one.
		sm.prefix, sm.prefixLen = ipDB.getIP(sm.name)
	}

	glog.V(5).Infof("Setting prefix %s/%d for %s", sm.prefix, sm.prefixLen, sm.name)

	// If we're here, it means we need to reconfigure the interface.
	if err := setPrefix(sm.ctx, sm.client, sm.name, sm.prefix, sm.prefixLen); err != nil {
		glog.Errorf("Error setting prefix %s/%d for %s: %v", sm.prefix, sm.prefixLen, sm.name, err)
	}
}

// Event handler for "adminStatus" events.
// If the event's value is "UP", transition the machine to "adminUp", and if we received the
// configuration for the interface, transition it further to "configured".
func (sm *interfaceSm) adminStatusEventHandler(ev *event, value string) {
	// Since the event handlers can be called from multiple threads (timer and event loop),
	// we need to lock the object while we perform our operations.
	sm.mu.Lock()
	// Unlock when we're returning from this function.
	defer sm.mu.Unlock()

	glog.V(5).Infof("Handling adminStatus %s for %s", value, sm.name)

	switch value {
	case "UP":
		if sm.state > adminDown {
			return
		}

		sm.adminUp()

		if sm.configComplete() {
			sm.configured()
		}
	case "DOWN":
		sm.adminDown()
	default:
		glog.Errorf("Unknown admin state: %s", value)
	}

	glog.V(5).Infof("New state for %s: %s", sm.name, stateName(sm.state))
}

// Event handler for "prefixEvent".
// We store the value and if the state machine is in "adminUp" state, and we received all the
// configuration we transition to "configured" state.
// Otherwise we do nothing.
func (sm *interfaceSm) prefixEventHandler(ev *event, value string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	glog.V(5).Infof("Handling prefix %s for %s", value, sm.name)

	sm.prefix = value

	if sm.state != adminUp {
		return
	}

	if sm.configComplete() {
		sm.configured()
	}

	glog.V(5).Infof("New state for %s: %s", sm.name, stateName(sm.state))
}

// Event handler for "prefixLenEvent".
// We store the value and if the state machine is in "adminUp" state, and we received all the
// configuration we transition to "configured" state.
// Otherwise we do nothing.
func (sm *interfaceSm) prefixLenEventHandler(ev *event, value string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	glog.V(5).Infof("Handling prefix length %s for %s", value, sm.name)

	// Convert the value from string to integer.
	prefixLen, err := strconv.Atoi(value)
	if err != nil {
		glog.Errorf("Invalid prefix len: %s", value)
	}
	sm.prefixLen = prefixLen

	if sm.state != adminUp {
		return
	}

	if sm.configComplete() {
		sm.configured()
	}

	glog.V(5).Infof("New state for %s: %s", sm.name, stateName(sm.state))
}

// We handle the "timerEvent".
// If the state machine is in "adminUp", we transition it to "configured" state.
// Otherwise we do nothing.
func (sm *interfaceSm) timerEventHandler() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	glog.V(5).Infof("Handling timer for %s", sm.name)

	if sm.state != adminUp {
		return
	}
	sm.timer = nil

	sm.configured()

	glog.V(5).Infof("New state for %s: %s", sm.name, stateName(sm.state))
}
