package main

import (
	"fmt"

	"github.com/aristanetworks/glog"
)

// A global variable that holds our IP DB.
var ipDB ipDBManager

// A dumb demo IP database that holds a fixed number of IPs with a fixed prefix length.
type ipDBManager struct {
	// A map from IP to the interface it's assigned to.
	// An empty string value means "not assigned".
	ips map[string]string
	// A map from interface to its assigned IP.
	// Entries exist only for interfaces that have assigned IPs.
	interfaces map[string]string
	// The prefix length for IPs.
	prefixLen int
}

// Adds "n" IP addresses (of the form "10.0.<i>.1") to the database and sets the prefix length.
func initIPs(n int, prefixLen int) {
	ipDB.ips = make(map[string]string)
	ipDB.interfaces = make(map[string]string)
	ipDB.prefixLen = prefixLen

	for i := 1; i <= n; i++ {
		k := fmt.Sprintf("10.0.%d.1", i)
		ipDB.ips[k] = ""
	}
}

// Gets the IP for the interface.
// If the interface already has an IP assigned, it will return it. Otherwise, it will assign it
// a new one.
func (db *ipDBManager) getIP(iface string) (string, int) {
	if addr, ok := db.interfaces[iface]; ok {
		return addr, db.prefixLen
	}

	for k, v := range db.ips {
		if v == "" {
			db.ips[k] = iface
			db.interfaces[iface] = k
			return k, db.prefixLen
		}
	}
	glog.Error("IP addresses exhausted")

	return "1.1.1.1", 32
}

// Reconciles the IP and prefix length for the specified interface with the database.
// It returns an IP, prefix length, and whether the given IP and prefix length are consistent with
// the database (ie, interface doesn't need to be reconfigured).
// Reconciliation is needed because we don't want to needlessly reconfigure an interface
// that already has its assigned address already configured, or its configured address is not
// currently allocated to another interface.
func (db *ipDBManager) reconcile(iface string, ip string, prefixLen int) (string, int, bool) {
	// Try to keep the given IP and prefix length if possible (ie, if not assigned or assigned to
	// the same inteface).
	if v, ok := db.ips[ip]; ok && (v == "" || v == iface) {
		db.ips[ip] = iface
		db.interfaces[iface] = ip
		return ip, db.prefixLen, prefixLen == db.prefixLen
	}
	// Otherwise, assign a new one.
	ip, prefixLen = db.getIP(iface)
	return ip, prefixLen, false

}

// Marks the IP as unassigned.
func (db *ipDBManager) releaseIP(ip string) {
	if iface, ok := db.ips[ip]; ok {
		delete(db.interfaces, iface)
	}
	db.ips[ip] = ""
}
