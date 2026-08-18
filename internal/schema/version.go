package schema

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AD-21: compatibility is judged against the binary on the reader's machine,
// never against the file's `version:` field.
//
// The file's field is obsolete — Compose itself ignores it and warns about it —
// so filtering the schema by it would hide keys that work perfectly on the
// installed Compose. The installed binary is the only thing that can actually
// refuse to run a key, so it is the only thing consulted.

// composeProbeTimeout bounds the probe. `docker compose version` shells out to
// the Docker CLI, which on a machine with an unreachable daemon context can sit
// for a long time. The inspector must not wait on it: a probe that has not
// answered is the same answer as no binary at all — nothing is marked.
const composeProbeTimeout = 3 * time.Second

var (
	probeOnce    sync.Once
	probeVersion string
	probeKnown   bool
)

// InstalledCompose returns the version of `docker compose` on this machine and
// whether one was found. Probed at most once per process.
//
// Not finding one is a normal, supported state, not a failure: with no binary
// nothing is marked and the whole schema is offered. We degrade to useful.
func InstalledCompose() (string, bool) {
	probeOnce.Do(func() {
		probeVersion, probeKnown = probeCompose()
	})
	return probeVersion, probeKnown
}

func probeCompose() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), composeProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "compose", "version", "--short").Output()
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(out))
	// `--short` prints "2.29.1"; older builds print a leading v.
	v = strings.TrimPrefix(v, "v")
	if v == "" || !isVersionish(v) {
		return "", false
	}
	return v, true
}

func isVersionish(v string) bool {
	if v == "" || (v[0] < '0' || v[0] > '9') {
		return false
	}
	return true
}

// supportOf decides AD-21's mark for one key.
//
// Three outcomes and only one of them hides anything, which is none of them.
// SupportNo is a MARK on a key that is still listed: the reader learns the key
// exists and that upgrading would give it to them, which is strictly more than
// either hiding it or offering it silently.
func supportOf(minVersion, installed string, installedKnown bool) Support {
	if minVersion == "" || !installedKnown {
		return SupportUnknown
	}
	switch compareVersions(installed, minVersion) {
	case -1:
		return SupportNo
	default:
		return SupportYes
	}
}

// compareVersions compares dotted numeric versions, ignoring any pre-release
// suffix. Returns -1, 0 or 1.
//
// A suffix is dropped rather than ordered: "2.30.0-rc.1" against a minimum of
// "2.30.0" answers "yes". A release candidate of the version that introduced a
// key has the key, and the alternative — marking it unsupported — would tell a
// reader on an rc build that a key they can use does not exist here.
func compareVersions(a, b string) int {
	as, bs := versionParts(a), versionParts(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, seg := range strings.Split(v, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(seg))
		if err != nil {
			// A segment that is not a number ends the comparison rather than
			// counting as zero: "2.x" must not read as "2.0".
			break
		}
		out = append(out, n)
	}
	return out
}
