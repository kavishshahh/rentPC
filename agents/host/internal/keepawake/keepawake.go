// Package keepawake stops the host from sleeping while rentals are running.
// A sleeping host pauses its VMs and drops the relay connection, which the
// renter experiences as an outage.
package keepawake

import "sync"

var mu sync.Mutex
var active bool

// Set turns sleep prevention on or off. Idempotent; call it from a ticker.
func Set(on bool) {
	mu.Lock()
	defer mu.Unlock()
	if on == active {
		if on {
			poke() // some platforms need a periodic nudge
		}
		return
	}
	active = on
	if on {
		start()
	} else {
		stop()
	}
}
