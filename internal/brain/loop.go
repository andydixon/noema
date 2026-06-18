package brain

// loopDetector spots candidates that keep being admitted without the world
// changing (T20). It keeps a ring of recent admitted fingerprints; when one
// recurs threshold times within the window it is suppressed and a
// metacognition warning is raised once per streak.
type loopDetector struct {
	window    int
	threshold int
	ring      []string
	warned    map[string]bool
}

func newLoopDetector(window, threshold int) *loopDetector {
	return &loopDetector{window: window, threshold: threshold, warned: map[string]bool{}}
}

func fingerprint(c Candidate) string { return c.Process + "|" + c.Kind + "|" + c.Subject }

// count returns how many times fp appears in the window.
func (l *loopDetector) count(fp string) int {
	n := 0
	for _, x := range l.ring {
		if x == fp {
			n++
		}
	}
	return n
}

// looping reports whether admitting fp again would exceed the threshold.
func (l *loopDetector) looping(fp string) bool { return l.count(fp) >= l.threshold }

// record notes an admission.
func (l *loopDetector) record(fp string) {
	l.ring = append(l.ring, fp)
	if len(l.ring) > l.window {
		l.ring = l.ring[len(l.ring)-l.window:]
	}
	if l.count(fp) < l.threshold {
		delete(l.warned, fp)
	}
}

// shouldWarn returns true the first time a fingerprint is suppressed in a streak.
func (l *loopDetector) shouldWarn(fp string) bool {
	if l.warned[fp] {
		return false
	}
	l.warned[fp] = true
	return true
}

// snapshot exports the ring for persistence.
func (l *loopDetector) snapshot() []string { return append([]string{}, l.ring...) }

func (l *loopDetector) restore(ring []string) { l.ring = append([]string{}, ring...) }
