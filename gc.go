package flux

import "time"

type GCOptions struct {
	Enabled bool
	GraceMs int
}

func defaultGCOptions() GCOptions {
	return GCOptions{Enabled: true, GraceMs: 3000}
}

func (si *scopeImpl) maybeScheduleGC(atom AnyAtom) {
	if !si.gcOpts.Enabled || atom.atomKeepAlive() {
		return
	}

	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.Lock()
	if entry.state == StateIdle || entry.state == StateResolving {
		entry.mu.Unlock()
		return
	}
	if entry.pendingInvalidate || entry.pendingSet != nil {
		entry.mu.Unlock()
		return
	}
	si.invMu.Lock()
	pendingInvalidation := si.hasPendingInvalidationLocked(atom.atomID())
	si.invMu.Unlock()
	if pendingInvalidation {
		entry.mu.Unlock()
		return
	}
	subCount := activeListenerCount(entry)
	if subCount > 0 || len(entry.dependents) > 0 || entry.gcTimer != nil {
		entry.mu.Unlock()
		return
	}
	entry.gcTimer = time.AfterFunc(time.Duration(si.gcOpts.GraceMs)*time.Millisecond, func() {
		si.executeGC(atom)
	})
	entry.mu.Unlock()
}

func (si *scopeImpl) cancelScheduledGC(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.Lock()
	if entry.gcTimer != nil {
		entry.gcTimer.Stop()
		entry.gcTimer = nil
	}
	entry.mu.Unlock()
}

func (si *scopeImpl) executeGC(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.Lock()
	entry.gcTimer = nil
	if entry.state == StateIdle || entry.state == StateResolving {
		entry.mu.Unlock()
		return
	}
	if entry.pendingInvalidate || entry.pendingSet != nil {
		entry.mu.Unlock()
		return
	}
	si.invMu.Lock()
	pendingInvalidation := si.hasPendingInvalidationLocked(atom.atomID())
	si.invMu.Unlock()
	if pendingInvalidation {
		entry.mu.Unlock()
		return
	}
	subCount := activeListenerCount(entry)
	if subCount > 0 || len(entry.dependents) > 0 || atom.atomKeepAlive() {
		entry.mu.Unlock()
		return
	}
	entry.mu.Unlock()

	_ = si.releaseByID(atom.atomID())
}

func activeListenerCount(entry *atomEntry) int {
	subCount := 0
	for _, ls := range entry.listeners {
		for _, l := range ls {
			if l != nil {
				subCount++
			}
		}
	}
	return subCount
}

func (si *scopeImpl) hasPendingInvalidationLocked(id uint64) bool {
	if si.invalidationChain != nil && si.invalidationChain[id] {
		return true
	}
	for _, atom := range si.invalidationQueue {
		if atom.atomID() == id {
			return true
		}
	}
	return false
}
